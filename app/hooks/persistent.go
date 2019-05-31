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
// Uses standarad datastore client library.

import (
	"fmt"

	"cloud.google.com/go/datastore"
	"golang.org/x/net/context"
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

// setRepoError sets a repo to statusErrpr with the given cause
func setRepoError(ctx context.Context, c *datastore.Client, user, repo, errorCause string) error {
	return modifyRepoData(ctx, c, user, repo, func(item *repoStorageData) {
		item.Status = statusError
		item.ErrorCause = errorCause
	})
}

func modifyRepoData(ctx context.Context, c *datastore.Client, user, repo string, f func(*repoStorageData)) error {
	_, err := c.RunInTransaction(ctx, func(txn *datastore.Transaction) error {
		key := makeRepoKey(user, repo)

		var item repoStorageData
		if err := c.Get(ctx, key, &item); err != nil {
			return err
		}

		f(&item)
		if _, err := c.Put(ctx, key, &item); err != nil {
			return err
		}
		return nil
	})
	return err
}

// getRepoData returns the data for a single repo
func getRepoData(ctx context.Context, c *datastore.Client, user, repo string) (result repoStorageData, err error) {
	key := makeRepoKey(user, repo)
	err = c.Get(ctx, key, &result)
	return result, err
}

func makeReposRootKey() *datastore.Key {
	return datastore.NameKey(
		emptyKind,
		storageReposPath,
		nil,
	)
}

func makeRepoKey(user, repo string) *datastore.Key {
	return datastore.NameKey(
		repoKind,
		fmt.Sprintf("%s/%s", user, repo),
		makeReposRootKey(),
	)
}
