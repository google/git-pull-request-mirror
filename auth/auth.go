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

// Package auth provides helper methods for generating clients for the GitHub API.
//
// This includes building authentication into the generated client (when applicable).
//
// Note that we don't provide username/password authentication; It's both insecure
// and more complex to implement.
package auth

import (
	"context"
	"fmt"
	"os"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

const (
	// TokenHelp is a human-friendly string that can be used to describe token requirements in usage messages.
	TokenHelp = `You can generate a token at: https://github.com/settings/tokens
Note that the 'public_repo' scope is needed for public repositories,
And the 'repo' scope is needed for private repositories.
`
)

// UnauthenticatedClient builds a github client that uses http.Client's default
// HTTP transport.
// The client will be insecure and extremely rate-limited; non-authenticated
// users are limited to 60 requests / hour.
func UnauthenticatedClient() *github.Client {
	return github.NewClient(nil)
}

// TokenClient takes an oauth token and returns an authenticated github client.
// The client is guaranteed to work.
func TokenClient(token string) *github.Client {
	httpClient := oauth2.NewClient(
		oauth2.NoContext,
		oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: token},
		),
	)

	githubClient := github.NewClient(httpClient)

	_, _, err := githubClient.Users.Get(context.TODO(), "")

	if err != nil {
		fmt.Println("Token error: ", err)
		fmt.Println(TokenHelp)
		os.Exit(1)
	}

	return githubClient
}
