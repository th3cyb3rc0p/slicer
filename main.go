package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	conf "github.com/mzfr/slicer/config"
	"github.com/mzfr/slicer/extractor"

	"github.com/beevik/etree"
	"github.com/common-nighthawk/go-figure"
	"github.com/spf13/viper"
)

func init() {
	flag.Usage = func() {
		h := []string{
			"Extract information from Manifest and strings of an APK\n",
			"Usage:",
			"\tslicer [OPTION] [Extracted APK directory]\n",
			"Options:",
			"\n  -d, --dir		path to jadx output directory",
			"  -o, --output		Name of the output file(not implemented)",
			" -nb, --no-banner	Don't Show Banner",
			"\nExamples:\n",
			"slicer -d /path/to/the/extract/apk",
		}

		fmt.Fprintf(os.Stderr, strings.Join(h, "\n"))
	}
}

// list all the files of a given directory
func visit(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			files = append(files, filepath.Base(path))
		}
		return nil
	})
	return files, err
}

func sendRequest(url string) (*http.Response, error) {
	req, err := http.Get(url)
	return req, err
}

// ConfigReader reads the configuration using viper
func ConfigReader() (*viper.Viper, error) {
	v := viper.New()
	// Set the file name of the configurations file
	v.SetConfigName("config")

	// Set the path to look for the configurations file
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.slicer/")
	v.SetConfigType("yml")

	if err := v.ReadInConfig(); err != nil {
		fmt.Printf("Error reading config file, %s", err)
	}

	var configuration conf.Configurations
	err := v.Unmarshal(&configuration)

	return v, err
}

//Get Intents of all those activites which are either
// exported or have some intent filters defined
func getIntents(intentFilters []*etree.Element) {
	var formatedIntent string
	for _, intents := range intentFilters {
		fmt.Println("\tIntent-filters:")
		for _, Type := range intents.ChildElements() {
			if Type.Tag == "data" {
				host := Type.SelectAttrValue("android:host", "*")
				scheme := Type.SelectAttrValue("android:scheme", "*")
				formatedIntent = fmt.Sprintf("\t\t - %s: %s://%s", Type.Tag, scheme, host)

			} else {
				formatedIntent = fmt.Sprintf("\t\t - %s: %s", Type.Tag, Type.SelectAttrValue("android:name", "no name"))
			}
			fmt.Println(formatedIntent)
		}
	}
}

// Check all the activities, receivers, broadcasts, if they are exported.
// If not exported then we check if the intent-filters are set
func exported(component *etree.Element) {
	exported := component.SelectAttrValue("android:exported", "none")

	if exported == "none" {
		// If the activity doesn't have android:exported defined
		// we check if it has any Intent-filerts, if yes that means
		// exported by default
		if intentFilter := component.SelectElements("intent-filter"); intentFilter != nil {
			fmt.Printf("\t%s:", component.SelectAttrValue("android:name", "name not defined"))
			fmt.Println("\n\tPermission:", component.SelectAttrValue("android:permission", "null"))
			getIntents(intentFilter)
		}
	} else if exported == "true" {
		fmt.Printf("\t%s:", component.SelectAttrValue("android:name", "name not defined"))
		fmt.Println("\n\tPermission:", component.SelectAttrValue("android:permission", "null"))

		if intentFilter := component.SelectElements("intent-filter"); intentFilter != nil {
			getIntents(intentFilter)
		}
	}
}

// Parse the AndroidManifest.xml file
func parseManifest(document *etree.Document) {
	root := document.SelectElement("manifest")
	for _, app := range root.SelectElements("application") {
		// Check if the backup is allowed or not
		backup := app.SelectAttrValue("android:allowBackup", "true")
		fmt.Println("Backup allowed: ", backup)

		//Check if the app is set debuggable
		debuggable := app.SelectAttrValue("android:debuggable", "false")
		fmt.Println("Debuggable: ", debuggable)

		var attackSurface = []string{"activity", "receiver", "service", "provider"}
		for _, com := range attackSurface {
			fmt.Printf("\n%s:\n", com)
			for _, components := range app.SelectElements(com) {
				exported(components)
			}
		}
	}
}

// Parse the /res/values/strings.xml
func parseStrings(document *etree.Document, googleURL interface{}) {
	root := document.SelectElement("resources")

	for _, str := range root.SelectElements("string") {
		strValues := str.SelectAttrValue("name", "none")
		// Get Firebase DB URL and check if /.json trick works or not
		if strValues == "firebase_database_url" {
			firebaseURL := fmt.Sprintf("%s/.json", str.Text())
			req, err := sendRequest(firebaseURL)
			if err != nil {
				fmt.Println("Couldn't connect to Firebase")
				continue
			}
			if req.StatusCode == 401 {
				fmt.Printf("\n\t- %s: Permission Denied", firebaseURL)
				fmt.Println()
			} else {
				fmt.Printf("\n\t- %s: Is open to public", firebaseURL)
				fmt.Println()
			}
		}

		// Get Google API keys and see if they are restricted or not
		if strValues == "google_api_key" || strValues == "google_map_keys" {
			for _, keys := range googleURL.([]interface{}) {
				for _, values := range keys.(map[interface{}]interface{}) {
					requestURL := fmt.Sprintf("%s%s", values, str.Text())

					req, err := sendRequest(requestURL)
					if err != nil {
						fmt.Println("Unable to connect to the the google map")
						continue
					}
					body, err := ioutil.ReadAll(req.Body)
					if err != nil {
						return
					}

					if req.StatusCode != 403 && !strings.Contains(string(body), "API project is not authorized") && str.Text() != "" {
						fmt.Printf("\n\t- %s: %d\n", requestURL, req.StatusCode)
					}
				}
			}
		}

		// Some keys that I have found in loads of strings.xml and they have nothing important
		// so just filter those out.
		if strings.Contains(strings.ToLower(strValues), "api") && str.Text() != "" && strings.Contains(strings.ToLower(strValues), "key") {
			if strValues == "abc_capital_off" || strValues == "abc_capital_on" || strValues == "currentApiLevel" {
				continue
			}
			fmt.Printf("\t- %s: %s\n", strValues, str.Text())
		}
	}
}

func main() {
	var dir string
	flag.StringVar(&dir, "d", "", "")

	var banner bool
	flag.BoolVar(&banner, "nb", true, "")
	flag.Parse()

	if banner {
		myFigure := figure.NewColorFigure("# Slicer", "big", "green", true)
		myFigure.Print()
		fmt.Println()
	}

	v, _ := ConfigReader()

	paths := v.Get("paths")
	googleURL := v.Get("URLs")

	for _, key := range paths.([]interface{}) {
		for _, file := range key.(map[interface{}]interface{}) {
			filePath := fmt.Sprintf("%s/%s", dir, file)
			fi, err := os.Stat(filePath)
			if err != nil {
				fmt.Println(err)
				return
			}
			switch mode := fi.Mode(); {
			case mode.IsRegular():
				doc := etree.NewDocument()
				if err := doc.ReadFromFile(filePath); err != nil {
					panic(err)
				}
				if err := doc.SelectElement("manifest"); err != nil {
					parseManifest(doc)
					fmt.Printf("%s:\n", "Apikeys-in-manifest")
					for _, elem := range doc.FindElements("./manifest/application/meta-data") {
						name := elem.SelectAttrValue("android:name", "")
						if name != "" && strings.Contains(strings.ToLower(name), "api") {
							values := elem.SelectAttrValue("android:value", "none")
							fmt.Printf("\t- %s: %s\n", name, values)
						}
					}
				} else {
					fmt.Printf("%s:\n", "Strings")
					parseStrings(doc, googleURL)
				}
			// Just listing files of some directories
			case mode.IsDir():
				if filepath.Base(filePath) == "xml" {
					fmt.Printf("%s:\n", "XML-files")
				} else {
					fmt.Printf("%s:\n", "raw-files")
				}
				files, err := visit(filePath)
				if err != nil {
					fmt.Println(err)
				}
				for _, file := range files {
					fmt.Printf("\t- %s\n", file)
				}
			}
		}
	}
	// This should extract all the URLs
	extractor.Extract(dir)
}
