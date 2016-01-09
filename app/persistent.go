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

// Storage for persistent repository metadata: what they are, what their keys
// are, etc.
// Uses the appengine datastore.

import (
	"fmt"

	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
)

type repoStorageData struct {
	User       string
	Repo       string
	Token      string // TODO(jhgilles): add another layer of encryption here?
	HookID     int
	HookSecret string
	Status     string
	ErrorCause string
}

type repoExistsError struct {
	User string
	Repo string
}

func (e *repoExistsError) Error() string {
	return fmt.Sprintf("Already tracking repo: %s/%s, can't initialize",
		e.User,
		e.Repo,
	)
}

const (
	repoKind  = "repo"
	emptyKind = "empty"

	storageReposPath = "repos"

	statusValidating        = "Validating"         // Verifying repo w/ github
	statusInitializing      = "Initializing"       // Performing initial pull-all
	statusHooksInitializing = "Hooks Initializing" // Setting up hooks
	statusReady             = "Ready"              // Ready and waiting for hooks
	statusError             = "Error"              // Hit an unrecoverable error
)

func initStorage() error {
	c, done := context.WithCancel(appengine.BackgroundContext())
	defer done()

	rootKey := makeReposRootKey(c)
	_, err := datastore.Put(c, rootKey, &struct{}{})
	return err
}

// initRepoData is called to declare a new active repository in the
// datastore. It should run after the repo has been verified to work.
func initRepoData(c context.Context, user, repo, token string) error {
	item := repoStorageData{
		User:   user,
		Repo:   repo,
		Token:  token,
		Status: statusValidating,
	}
	key := makeRepoKey(c, user, repo)
	return datastore.RunInTransaction(c, func(c context.Context) error {
		var currentItem repoStorageData
		err := datastore.Get(c, key, &currentItem)

		if err != datastore.ErrNoSuchEntity {
			if err != nil {
				return err
			}
			return &repoExistsError{
				User: user,
				Repo: repo,
			}
		}

		_, err = datastore.Put(c, key, &item)
		return err
	}, &datastore.TransactionOptions{})
}

func modifyRepoData(c context.Context, user, repo string, f func(*repoStorageData)) error {
	return datastore.RunInTransaction(c, func(c context.Context) error {
		key := makeRepoKey(c, user, repo)

		var item repoStorageData

		err := datastore.Get(c, key, &item)
		if err != nil {
			return err
		}

		f(&item)

		_, err = datastore.Put(c, key, &item)

		return err
	}, &datastore.TransactionOptions{})
}

// setRepoError sets a repo to statusErrpr with the given cause
func setRepoError(c context.Context, user, repo, errorCause string) error {
	return modifyRepoData(c, user, repo, func(item *repoStorageData) {
		item.Status = statusError
		item.ErrorCause = errorCause
	})
}

// deleteRepoData does exactly what you'd expect.
func deleteRepoData(c context.Context, user, repo string) error {
	key := makeRepoKey(c, user, repo)
	return datastore.Delete(c, key)
}

// getRepoData returns the data for a single repo
func getRepoData(c context.Context, user, repo string) (result repoStorageData, err error) {
	key := makeRepoKey(c, user, repo)
	err = datastore.Get(c, key, &result)
	return
}

// getAllRepoData returns all active or errored repos.
func getAllRepoData(c context.Context) ([]repoStorageData, error) {
	rootKey := makeReposRootKey(c)
	q := datastore.NewQuery(repoKind).Ancestor(rootKey)
	it := q.Run(c)
	current := new(repoStorageData)
	result := []repoStorageData{}

	var err error

	for _, err = it.Next(current); err == nil; _, err = it.Next(current) {
		result = append(result, *current)
	}

	if err != datastore.Done {
		return nil, err
	}

	return result, nil
}

func makeReposRootKey(c context.Context) *datastore.Key {
	return datastore.NewKey(
		c,
		emptyKind,
		storageReposPath,
		0,
		nil,
	)
}

func makeRepoKey(c context.Context, user, repo string) *datastore.Key {
	return datastore.NewKey(
		c,
		repoKind,
		fmt.Sprintf("%s/%s", user, repo),
		0,
		makeReposRootKey(c),
	)
}
