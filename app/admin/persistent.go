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
	"google.golang.org/appengine/datastore"
)

type repoStorageData struct {
	User       string
	Repo       string
	Token      string // TODO(jhgilles): add another layer of encryption here?
	HookID     int64
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

func initStorage(ctx context.Context) error {
	ctx, done := context.WithCancel(ctx)
	defer done()

	rootKey := makeReposRootKey(ctx)
	_, err := datastore.Put(ctx, rootKey, &struct{}{})
	return err
}

// initRepoData is called to declare a new active repository in the
// datastore. It should run after the repo has been verified to work.
func initRepoData(ctx context.Context, user, repo, token string) error {
	item := repoStorageData{
		User:   user,
		Repo:   repo,
		Token:  token,
		Status: statusValidating,
	}
	key := makeRepoKey(ctx, user, repo)
	return datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		var currentItem repoStorageData
		err := datastore.Get(ctx, key, &currentItem)

		if err != datastore.ErrNoSuchEntity {
			if err != nil {
				return err
			}
			return &repoExistsError{
				User: user,
				Repo: repo,
			}
		}

		_, err = datastore.Put(ctx, key, &item)
		return err
	}, &datastore.TransactionOptions{})
}

func modifyRepoData(ctx context.Context, user, repo string, f func(*repoStorageData)) error {
	return datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		key := makeRepoKey(ctx, user, repo)

		var item repoStorageData

		err := datastore.Get(ctx, key, &item)
		if err != nil {
			return err
		}

		f(&item)

		_, err = datastore.Put(ctx, key, &item)

		return err
	}, &datastore.TransactionOptions{})
}

// setRepoError sets a repo to statusErrpr with the given cause
func setRepoError(ctx context.Context, user, repo, errorCause string) error {
	return modifyRepoData(ctx, user, repo, func(item *repoStorageData) {
		item.Status = statusError
		item.ErrorCause = errorCause
	})
}

// deleteRepoData does exactly what you'd expect.
func deleteRepoData(ctx context.Context, user, repo string) error {
	key := makeRepoKey(ctx, user, repo)
	return datastore.Delete(ctx, key)
}

// getRepoData returns the data for a single repo
func getRepoData(ctx context.Context, user, repo string) (result repoStorageData, err error) {
	key := makeRepoKey(ctx, user, repo)
	err = datastore.Get(ctx, key, &result)
	return
}

// getAllRepoData returns all active or errored repos.
func getAllRepoData(ctx context.Context) ([]repoStorageData, error) {
	rootKey := makeReposRootKey(ctx)
	q := datastore.NewQuery(repoKind).Ancestor(rootKey)
	it := q.Run(ctx)
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

func makeReposRootKey(ctx context.Context) *datastore.Key {
	return datastore.NewKey(
		ctx,
		emptyKind,
		storageReposPath,
		0,
		nil,
	)
}

func makeRepoKey(ctx context.Context, user, repo string) *datastore.Key {
	return datastore.NewKey(
		ctx,
		repoKind,
		fmt.Sprintf("%s/%s", user, repo),
		0,
		makeReposRootKey(ctx),
	)
}
