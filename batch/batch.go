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

// Package batch is the source for a command-line tool to pull all of the data
// from a Github repository and write it into a local clone of that repository.
//
// You need to clone the repository yourself (including the "refs/pull/*" refs)
// before running the tool.
//
// Example Usage:
//    git clone https://github.com/google/git-appraise git-appraise/
//    cd git-appraise
//    git fetch origin '+refs/pull/*:refs/pull/*'
//    ~/bin/github-mirror --target google/git-appraise --local ./ -auth-token <YOUR_AUTH_TOKEN>
//
// Note that the "-auth-token" flag is optional, but highly recommended. Without it
// your API requests will be throttled to 60 per hour.

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/google/git-appraise/repository"
	"github.com/google/go-github/github"

	"github-mirror/auth"
	"github-mirror/mirror"
)

var remoteRepository = flag.String("target", "", "Github repository to read data from")
var localRepositoryDir = flag.String("local", ".", "Local repository to write notes to")
var token = flag.String("auth-token", "", "Github OAuth token with either the `repo' or `public_repo' scopes: https://github.com/settings/tokens")
var quiet = flag.Bool("quiet", false, "Don't log information to stdout")

func usage(errorMessage string) {
	fmt.Fprintln(os.Stderr, errorMessage)
	flag.Usage()
	os.Exit(1)
}

func main() {
	flag.Parse()
	splitTarget := strings.Split(*remoteRepository, "/")
	if len(splitTarget) != 2 {
		usage("Target repository is required, in the format `user/repo'")
	}
	userName := splitTarget[0]
	repoName := splitTarget[1]

	localDirInfo, err := os.Stat(*localRepositoryDir)
	if err != nil {
		log.Fatal(err)
	}
	if !localDirInfo.IsDir() {
		usage("Local repository must be a directory")
	}

	local, err := repository.NewGitRepo(*localRepositoryDir)
	if err != nil {
		log.Fatal("Couldn't open local repository: ", err.Error(), "\n",
			"Make sure you clone the remote repository locally first!")
	}

	tokenAuth := *token != ""
	if !tokenAuth {
		fmt.Fprintln(os.Stderr, "Not using authentication. Note that this will be EXTREMELY SLOW;")
		fmt.Fprintln(os.Stderr, "you get 60 requests to the github API per hour.")
		fmt.Fprint(os.Stderr, auth.TokenHelp)
	}

	var client *github.Client
	if tokenAuth {
		client = auth.TokenClient(*token)
	} else {
		client = auth.UnauthenticatedClient()
	}

	_, _, err = client.Repositories.Get(userName, repoName)
	if err != nil {
		log.Fatal("Error fetching repository info: ", err.Error())
	}

	errOutput := make(chan error, 1000)
	nErrors := 0
	go func() {
		for err := range errOutput {
			if !*quiet {
				log.Println(err)
			}
			nErrors++
		}
	}()
	statuses, err := mirror.GetAllStatuses(userName, repoName, client, errOutput)
	if err != nil {
		log.Fatal("Error reading statuses: ", err.Error())
	}
	reviews, err := mirror.GetAllPullRequests(local, userName, repoName, client, errOutput)
	if err != nil {
		log.Fatal("Error reading pull requests: ", err.Error())
	}
	close(errOutput)

	nStatuses := len(statuses)
	nReviews := len(reviews)
	var l *log.Logger
	if *quiet {
		l = log.New(ioutil.Discard, "", 0)
	} else {
		l = log.New(os.Stdout, "", 0)
	}
	logChan := make(chan string, 1000)
	go func() {
		for msg := range logChan {
			l.Println(msg)
		}
	}()

	l.Printf("Done reading! Read %d statuses, %d PRs", nStatuses, nReviews)
	l.Printf("Committing...\n")
	if err := mirror.WriteNewReports(statuses, local, logChan); err != nil {
		log.Fatal(err)
	}
	if err := mirror.WriteNewReviews(reviews, local, logChan); err != nil {
		log.Fatal(err)
	}
	close(logChan)

	l.Printf("Done! Hit %d errors", nErrors)
	if nErrors > 0 {
		os.Exit(1)
	}
}
