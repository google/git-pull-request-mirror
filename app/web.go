/*
Copyright 2015 Google Inc. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/user"
)

// Code for the web control panel

const (
	// idRepoName is the id used in an http form for a repository
	idRepoName = "repoName"
	// idRepoToken is the id used in an http form for a github API key
	idRepoToken = "repoToken"
)

var configTemplate = template.Must(template.ParseFiles("/go/src/app/templates/config.tmpl"))

// renderRepo represents a single repository to be rendered on the page
type renderRepo struct {
	Name       string
	Status     string
	ErrorCause string
}

// renderConfig is the top-level struct passed to rendering
type renderConfig struct {
	Repos []renderRepo
}

// configHandler renders a configuration page
func configHandler(w http.ResponseWriter, req *http.Request) {
	c := appengine.NewContext(req)

	repos, err := getAllRepoData(appengine.NewContext(req))

	if err != nil {
		log.Errorf(c, "Error fetching repos: %s", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	conf := renderConfig{}

	for _, repo := range repos {
		conf.Repos = append(conf.Repos, renderRepo{
			Name:       fmt.Sprintf("%s/%s", repo.User, repo.Repo),
			Status:     repo.Status,
			ErrorCause: repo.ErrorCause,
		})
	}

	configTemplate.Execute(w, &conf)
}

// addHandler handles POSTs to the /add endpoint
func addHandler(w http.ResponseWriter, req *http.Request) {
	defer http.Redirect(w, req, "/", http.StatusSeeOther)
	c := appengine.NewContext(req)

	if req.Method != "POST" {
		log.Errorf(c, "Incorrect method for /add endpoint: %s", req.Method)
		return
	}

	err := req.ParseForm()
	if err != nil {
		log.Errorf(c, "Couldn't parse form for /add endpoint: %s", err.Error())
		return
	}

	repoName := req.PostForm.Get(idRepoName)
	if repoName == "" {
		log.Errorf(c, "No repoName for /add endpoint: %v", req.PostForm)
		return
	}

	repoToken := req.PostForm.Get(idRepoToken)
	if repoToken == "" {
		log.Errorf(c, "No repoToken for /add endpoint: %v", req.PostForm)
		return
	}

	splitName := strings.Split(repoName, "/")
	if len(splitName) != 2 {
		log.Errorf(c, "Invalid repository name (can't split on '/'): %s", repoName)
		return
	}

	log.Infof(c, "Adding repository %s", repoName)

	err = initRepoData(c, splitName[0], splitName[1], repoToken)

	if err != nil {
		log.Errorf(c, "Couldn't store repository %s: %s", repoName, err.Error())
		return
	}

	go validate(splitName[0], splitName[1])
}

// deleteHandler handles POSTS to the /delete endpoint
func deleteHandler(w http.ResponseWriter, req *http.Request) {
	defer http.Redirect(w, req, "/", http.StatusSeeOther)
	c := appengine.NewContext(req)

	if req.Method != "POST" {
		log.Errorf(c, "Incorrect method for /delete endpoint: %s", req.Method)
		return
	}

	err := req.ParseForm()
	if err != nil {
		log.Errorf(c, "Couldn't parse form for /delete endpoint: %s", err.Error())
		return
	}

	fullRepoName := req.PostForm.Get(idRepoName)
	if fullRepoName == "" {
		log.Errorf(c, "No repoName for /delete endpoint: %v", req.PostForm)
		return
	}

	splitName := strings.Split(fullRepoName, "/")
	if len(splitName) != 2 {
		log.Errorf(c, "Invalid repository name (can't split on '/'): %s", fullRepoName)
		return
	}

	go deactivate(splitName[0], splitName[1])
}

// enforceLoginHandler wraps another handler, returning a handler that will
// enforce user login and then pass off control down the chain.
func enforceLoginHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c := appengine.NewContext(req)
		u := user.Current(c)
		if u == nil {
			// Not logged in
			url, err := user.LoginURL(c, req.URL.String())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Redirect(w, req, url, http.StatusSeeOther)
			return
		}
		// Pass off control
		next.ServeHTTP(w, req)
	})
}

func setupHandlers() {
	setupHookHandlers()
	http.Handle("/add", enforceLoginHandler(http.HandlerFunc(addHandler)))
	http.Handle("/delete", enforceLoginHandler(http.HandlerFunc(deleteHandler)))
	http.Handle("/", enforceLoginHandler(http.HandlerFunc(configHandler)))
}
