package src

import (
	"io"
	"fmt"
	"sort"
	"time"
	"bytes"
	"errors"
	"strconv"
	"strings"
	"net/http"
	"io/ioutil"
	"html/template"
	"encoding/json"
	"encoding/base64"

	"appengine"
	"appengine/user"
	"appengine/mail"
	"appengine/urlfetch"
	"appengine/datastore"
)

func init() {
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/style.css", stylesheetHandler)
	http.HandleFunc("/addClassToTrack", addClassHandler)
	http.HandleFunc("/removeClass", removeClassHandler)
	http.HandleFunc("/checkClasses", checkClassesHandler)
	http.HandleFunc("/getTermsAndSchools", getTermsAndSchoolsHandler)
	http.HandleFunc("/refreshAccessToken", refreshAccessTokenHandler)
	http.HandleFunc("/stats", statsHandler)
}

type Class struct {
	UserEmail	string
	TermCode	string
	SchoolCode	string
	Subject		string
	ClassNumber	string
	SectionNumber	string
	Status		bool
}

type Term struct {
	TermCode	string
	TermDescr	string
}

type School struct {
	TermCode	string
	SchoolCode	string
	SchoolDescr	string
}

type AuthInfo struct {
	AccessToken	string
	ConsumerKey	string
	ConsumerSecret	string
}

var baseUrl = "http://api-gw.it.umich.edu/Curriculum/SOC/v1"

//Handling hitting the home page: Checking the user and loading the info

var templates = template.Must(template.ParseFiles("website/home.html", "website/style.css", "website/stats.html"))

type ClassTableRow struct {
	TermCode	string
	SchoolCode	string
	Term		string
	Subject		string
	ClassNumber	string
	SectionNumber	string
	StatusColor	string
}

type TermWithSchools struct {
	TermCode	template.JS
	TermDescr	string
	FirstSchool	School
	Schools		[]School
}

type HomePageInflater struct {
	UserEmail	string
	Terms		[]Term
	ClassTableRows	[]ClassTableRow
	Version		string
}

//Some sorting definitions
type ByTermCode []Term
func (a ByTermCode) Len() int           { return len(a) }
func (a ByTermCode) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByTermCode) Less(i, j int) bool { return a[i].TermCode > a[j].TermCode }

type BySchoolName []School
func (a BySchoolName) Len() int           { return len(a) }
func (a BySchoolName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a BySchoolName) Less(i, j int) bool { return a[i].SchoolDescr < a[j].SchoolDescr }

func homeHandler(w http.ResponseWriter, r *http.Request) {
	didBlockUser := checkTheUserAndBlockIfNecessary(w, r)
	if(didBlockUser) {
		return
	}

	context := appengine.NewContext(r)
	currentUser := user.Current(context)

	termsQuery := datastore.NewQuery("Term")
	var terms []Term
	_, err := termsQuery.GetAll(context, &terms)
	if(err != nil) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	sort.Sort(ByTermCode(terms))

	classesQuery := datastore.NewQuery("Class").Filter("UserEmail =", currentUser.Email)
	var classes []Class
	_, err = classesQuery.GetAll(context, &classes)
	if(err != nil) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	classRows := make([]ClassTableRow, len(classes))

	for i, class := range classes {
		statusColor := "red"
		if(class.Status) {
			statusColor = "green"
		}

		classRows[i] = ClassTableRow { TermCode: class.TermCode,
					       SchoolCode: class.SchoolCode,
					       Subject: class.Subject,
					       ClassNumber: class.ClassNumber,
					       SectionNumber: class.SectionNumber,
					       StatusColor: statusColor,
					     }
	}

	homePageInflater := HomePageInflater { UserEmail: currentUser.Email,
					       Terms: terms,
					       ClassTableRows: classRows,
					       Version: "0.2.5",
					     }

	err = templates.ExecuteTemplate(w, "home.html", homePageInflater)
	if(err != nil) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func checkTheUserAndBlockIfNecessary(w http.ResponseWriter, r *http.Request) (bool) {
	context := appengine.NewContext(r)
	currentUser := user.Current(context)
	if(currentUser == nil) {
		url, _ := user.LoginURL(context, "/")
		http.Redirect(w, r, url, http.StatusFound)
		return true
	}

	return false
}

type StylesheetInflater struct {
	//Empty type so we can serve the stylesheet. Might be a goofy way of accomplishing this
}

func stylesheetHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css")
	stylesheetInflater := StylesheetInflater {}
	err := templates.ExecuteTemplate(w, "style.css", stylesheetInflater)
	if(err != nil) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

//Handling entering something on the form

func addClassHandler(w http.ResponseWriter, r *http.Request) {
	didBlockUser := checkTheUserAndBlockIfNecessary(w, r)
	if(didBlockUser) {
		return
	}

	context := appengine.NewContext(r)
	currentUser := user.Current(context)

	termCode := r.FormValue("TermCode")
	schoolCode := r.FormValue("SchoolCode")
	subject := strings.ToUpper(r.FormValue("Subject"))
	classNumber := r.FormValue("ClassNumber")
	sectionNumber := r.FormValue("SectionNumber")

	classToCheck :=  Class { UserEmail: currentUser.Email,
				 TermCode: termCode,
				 SchoolCode: schoolCode,
				 Subject: subject,
				 ClassNumber: classNumber,
				 SectionNumber: sectionNumber,
				 Status: false,
				}

	classInfo, err := loadClassInfoAndCheckValidity(context, classToCheck)
	if(err == nil) {
		classToCheck.Status = getClassStatusFromClassInfo(classInfo)
		_, err := datastore.Put(context, datastore.NewIncompleteKey(context, "Class", nil), &classToCheck)
		if(err != nil) {
			fmt.Fprintf(w, "There was a problem storing your class.")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else {
			time.Sleep(500 * time.Millisecond)
			http.Redirect(w, r, "/", http.StatusFound)
		}
	} else {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

//Handling removing classes

func removeClassHandler(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)
	classQuery := datastore.NewQuery("Class").KeysOnly()

	termCode := r.FormValue("TermCode")
	schoolCode := r.FormValue("SchoolCode")
	subject := strings.ToUpper(r.FormValue("Subject"))
	classNumber := r.FormValue("ClassNumber")
	sectionNumber := r.FormValue("SectionNumber")
	userEmail := r.FormValue("UserEmail")

	context.Infof("TermCode: " + termCode)
	context.Infof("SchoolCode: " + schoolCode)
	context.Infof("Subject: " + subject)
	context.Infof("ClassNumber: " + classNumber)
	context.Infof("SectionNumber: " + sectionNumber)
	context.Infof("UserEmail: " + userEmail)

	classQuery = classQuery.Filter("TermCode =", termCode)
	classQuery = classQuery.Filter("SchoolCode =", schoolCode)
	classQuery = classQuery.Filter("Subject =", subject)
	classQuery = classQuery.Filter("ClassNumber =", classNumber)
	classQuery = classQuery.Filter("SectionNumber =", sectionNumber)
	classQuery = classQuery.Filter("UserEmail =", userEmail)

	classKeys, err := classQuery.GetAll(context, nil)
	if(err != nil) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	context.Infof("len(classKeys): " + strconv.Itoa(len(classKeys)))

	if(len(classKeys) < 1) {
		fmt.Fprintf(w, "There was a problem finding your class.")
		http.Error(w, "Uh oh", http.StatusInternalServerError)
		return
	}

	err = datastore.Delete(context, classKeys[0])
	if(err != nil) {
		fmt.Fprintf(w, "There was a problem deleting your class.")
		http.Error(w, "Uh oh", http.StatusInternalServerError)
		return
	} else {
		time.Sleep(500 * time.Millisecond)
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

//Checking up on the classes

func checkClassesHandler(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)
	classesQuery := datastore.NewQuery("Class")

	var classes []Class
	classKeys, err := classesQuery.GetAll(context, &classes)
	if(err != nil) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i, class := range classes {
		classInfo, err := loadClassInfoAndCheckValidity(context, class)
		if(err == nil) {
			classStatus := getClassStatusFromClassInfo(classInfo)
			context.Infof("Status: ", classStatus)

			if(classStatus != class.Status) {
				context.Infof(" - Status changed, notifying " + class.UserEmail + "\n")
				sendEmailNotificationAboutStatusChange(context, class, classStatus)
			} else {
				context.Infof(" - Status hasn't changed\n")
			}
			class.Status = classStatus
			datastore.Put(context, classKeys[i], &class)
		} else {
			context.Infof("Error loading the info for a class: " + err.Error() + "\n")
		}
	}
}

type ClassOverallResponse struct {
	ClassInfo	ClassInformation `json:"getSOCSectionDetailResponse"`
}

type ClassInformation struct {
	AvailableSeats	string
}

func loadClassInfoAndCheckValidity(context appengine.Context, class Class) (ClassInformation, error) {
	bogusClassInfo := ClassInformation { AvailableSeats: "-1" }

	responseBody, err := runApiRequest(context, "/Terms/" + class.TermCode + "/Schools/" + class.SchoolCode + "/Subjects/" + class.Subject + "/CatalogNbrs/" + class.ClassNumber + "/Sections/" + class.SectionNumber)
	if(err != nil) {
		return bogusClassInfo, errors.New("Failed loading the class info!")
	}

	bodyString := string(responseBody)

	if(!strings.Contains(bodyString, "AvailableSeats")) {
		context.Infof("Loading class info failed, response body was: %s", bodyString)
		return bogusClassInfo, errors.New("Class doesn't exist!")
	}

	context.Infof("About to unmarshal: %s", string(responseBody))
	var classResponse ClassOverallResponse
	err = json.Unmarshal(responseBody, &classResponse);
	if(err != nil) {
		context.Infof("Couldn't unmarshal the class response")
		context.Infof(err.Error())
		return bogusClassInfo, err
	}

	return classResponse.ClassInfo, nil
}

func getClassStatusFromClassInfo(classInfo ClassInformation) (bool) {
	return (classInfo.AvailableSeats != "0")
}

func sendEmailNotificationAboutStatusChange(context appengine.Context, class Class, newStatus bool) {
	var statusMessage string
	if(newStatus) {
		statusMessage = " opened up! Register as soon as you can!"
	} else {
		statusMessage = " filled up! Crap. Sorry."
	}

	msg := &mail.Message {
				Sender:		"Umich Class Checker <umclasschecker@gmail.com>",
				To:		 []string{class.UserEmail},
				Subject:	"Umich Class Status Change",
				Body:		"Hey!\n\n" +
						"The Umich Class Checker noticed that " + class.Subject + " " + class.ClassNumber + ", section " + class.SectionNumber + statusMessage + "\n\n" +
						"Have a good one!",
		   }

	mail.Send(context, msg)
}

//Getting the latest information on terms and schools

func getTermsAndSchoolsHandler(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	//Request all the terms
	terms, err := getAndStoreTerms(context)
	if(err != nil) {
		context.Infof("Failed to load and store the terms")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	//For each term, request schools and store them in the datastore
	clearSchoolsFromDatastore(context)
	for _,term := range terms {
		err = getAndStoreSchoolsForTerm(context, term)
		if(err != nil) {
			context.Infof("Failed to load and store the schools")
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

//Getting and storing terms

type TermsOverallResponse struct {
	OverallResponse TermsResponse `json:"getSOCTermsResponse"`
}

type TermsResponse struct {
	Terms	[]Term `json:"Term"`
}

func getAndStoreTerms(context appengine.Context) ([]Term, error) {
	responseBody, err := runApiRequest(context, "/Terms")
	if(err != nil) {
		context.Infof("Failed loading the terms!")
		context.Infof(err.Error())
		return nil, err
	}

	context.Infof("About to unmarshal: %s", string(responseBody))
	var termsResponse TermsOverallResponse
	err = json.Unmarshal(responseBody, &termsResponse);
	if(err != nil) {
		context.Infof("Couldn't unmarshal the terms response")
		context.Infof(err.Error())
		return nil, err
	}

	termsQuery := datastore.NewQuery("Term").KeysOnly()
	termKeys, err := termsQuery.GetAll(context, nil)
	if(err != nil) {
		context.Infof("There was a problem loading the existing terms from the datastore")
		context.Infof(err.Error())
		return nil, err
	}
	for _,termKey := range termKeys {
		datastore.Delete(context, termKey)
	}

	for _,term := range termsResponse.OverallResponse.Terms {
		datastore.Put(context, datastore.NewIncompleteKey(context, "Term", nil), &term)
	}

	return termsResponse.OverallResponse.Terms, nil
}

//Getting and storing schools

type SchoolsOverallResponse struct {
	OverallResponse SchoolsResponse `json:"getSOCSchoolsResponse"`
}

type SchoolsResponse struct {
	Schools []School `json:"School"`
}

func clearSchoolsFromDatastore(context appengine.Context) {
	schoolsQuery := datastore.NewQuery("School").KeysOnly()
	schoolKeys, err := schoolsQuery.GetAll(context, nil)
	if(err != nil) {
		context.Infof("There was a problem loading the existing schools from the datastore")
		context.Infof(err.Error())
		return
	}
	for _,schoolKey := range schoolKeys {
		datastore.Delete(context, schoolKey)
	}
}

func getAndStoreSchoolsForTerm(context appengine.Context, term Term) (error) {
	responseBody, err := runApiRequest(context, "/Terms/" + term.TermCode + "/Schools/")
	if(err != nil) {
		context.Infof("Failed loading the schools!")
		context.Infof(err.Error())
		return err
	}

	context.Infof("About to unmarshal: %s", string(responseBody))
	var schoolsResponse SchoolsOverallResponse
	err = json.Unmarshal(responseBody, &schoolsResponse);
	if(err != nil) {
		context.Infof("Couldn't unmarshal the schools response")
		context.Infof(err.Error())
		return err
	}

	for _,school := range schoolsResponse.OverallResponse.Schools {
		school.TermCode = term.TermCode
		datastore.Put(context, datastore.NewIncompleteKey(context, "School", nil), &school)
	}

	return nil
}

//API stuff

func runApiRequest(context appengine.Context, path string) ([]byte, error) {
	_, authInfos, err := readAuthInfoFromDatastore(context)
	if(err != nil) {
		context.Infof("Failed to load the auth info")
		return nil, err
	}

	requestUrl := baseUrl + path;
	auth := "Bearer " + authInfos[0].AccessToken

	client := urlfetch.Client(context)
	request, err := http.NewRequest("GET", requestUrl, nil)
	request.Header.Add("Authorization", auth)
	request.Header.Add("Accept", "application/json")

	context.Infof("About to run request at %s", requestUrl)
	response, err := client.Do(request)

	if(err != nil) {
		context.Infof("Request failed! %s", err.Error())
		return nil, err
	}

	body, err := ioutil.ReadAll(response.Body)
	response.Body.Close()

	if(err != nil) {
		context.Infof("Couldn't read the response body!")
		return nil, err
	}

	return body, nil
}

func readAuthInfoFromDatastore(context appengine.Context) ([]*datastore.Key, []AuthInfo, error) {
	authInfoQuery := datastore.NewQuery("AuthInfo")
	var authInfos []AuthInfo
	authInfoKeys, err := authInfoQuery.GetAll(context, &authInfos)
	if(err != nil) {
		return nil, nil, err
	}

	return authInfoKeys, authInfos, nil
}

type RefreshAccessTokenResponse struct {
	AccessToken	string `json:"access_token"`
}

type RequestBodyCloser struct {
	io.Reader
}
func (RequestBodyCloser) Close() error { return nil }

func refreshAccessTokenHandler(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	authInfoKeys, authInfos, err := readAuthInfoFromDatastore(context)
	if(err != nil) {
		context.Infof("Failed to load the auth info")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if(len(authInfos) < 1) {
		//If there isn't any auth info in the datastore,
		//make a blank one so I can fill it in manually later
		blankAuthInfo := AuthInfo { AccessToken:	"blank",
					    ConsumerKey:	"blank",
					    ConsumerSecret:	"blank",
					}
		datastore.Put(context, datastore.NewIncompleteKey(context, "AuthInfo", nil), &blankAuthInfo)
		return
	}

	authInfo := authInfos[0]

	unencodedBasicAuthString := authInfo.ConsumerKey + ":" + authInfo.ConsumerSecret
	encodedBasicAuth := base64.StdEncoding.EncodeToString([]byte(unencodedBasicAuthString))
	encodedBasicAuth = "Basic " + encodedBasicAuth

	client := urlfetch.Client(context)
	request, err := http.NewRequest("POST", "https://api-km.it.umich.edu/token", nil)
	request.Header.Add("Authorization", encodedBasicAuth)
	request.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	request.Body = RequestBodyCloser{bytes.NewBufferString("grant_type=client_credentials&scope=PRODUCTION")}

	response, err := client.Do(request)

	if(err != nil) {
		context.Infof("Request to get a new access token failed!")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := ioutil.ReadAll(response.Body)
	response.Body.Close()

	if(err != nil) {
		context.Infof("Couldn't read the response body!")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	context.Infof("About to unmarshal: %s", string(body))
	var refreshAccessTokenResponse RefreshAccessTokenResponse
	err = json.Unmarshal(body, &refreshAccessTokenResponse)
	if(err != nil) {
		context.Infof("Couldn't unmarshal the response!")
		context.Infof(err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	authInfo.AccessToken = refreshAccessTokenResponse.AccessToken
	datastore.Put(context, authInfoKeys[0], &authInfo)

	context.Infof("Successfully refreshed the access token!")
}

//Stats stuff

type StatsInflater struct {
	UserEmail			string
	ClassesTracked			int
	MostTrackedClass		string
	MostTrackedClassTrackers	int
	NumUsers			int
	ClassesTrackedByUserWithMost	int
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)
	currentUser := user.Current(context)
	classesQuery := datastore.NewQuery("Class")

	var classes []Class
	_, err := classesQuery.GetAll(context, &classes)
	if(err != nil) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	statsInflater := StatsInflater { UserEmail: currentUser.Email,
					 ClassesTracked: 0,
					 MostTrackedClass: "",
					 MostTrackedClassTrackers: 0,
					 NumUsers: 0,
					 ClassesTrackedByUserWithMost: 0,
					 }

	classesSeen := map[string]int {}
	usersSeen := map[string]int {}

	for _, class := range classes {
		fullClassName := class.Subject + " " + class.ClassNumber
		classesSeen[fullClassName] = classesSeen[fullClassName] + 1
		usersSeen[class.UserEmail] = usersSeen[class.UserEmail] + 1
	}

	statsInflater.ClassesTracked = len(classesSeen)
	statsInflater.NumUsers = len(usersSeen)

	userWithMostClasses := ""
	classWithMostTrackers := ""

	for userEmail, classesTracked := range usersSeen {
		if(len(userWithMostClasses) <= 0 || classesTracked > usersSeen[userWithMostClasses]) {
			userWithMostClasses = userEmail
		}
	}

	statsInflater.ClassesTrackedByUserWithMost = usersSeen[userWithMostClasses]

	for className, trackers := range classesSeen {
		if(len(classWithMostTrackers) <= 0 || trackers > classesSeen[classWithMostTrackers]) {
			classWithMostTrackers = className;
		}
	}

	statsInflater.MostTrackedClass = classWithMostTrackers;
	statsInflater.MostTrackedClassTrackers = classesSeen[classWithMostTrackers];

	err = templates.ExecuteTemplate(w, "stats.html", statsInflater)
	if(err != nil) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
