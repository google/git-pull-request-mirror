# Mirror Github Pull Requests into the git-appraise formats

This repo contains a tool to mirror pull requests metadata into the corresponding git
repository using a feature of git called [git-notes](http://git-scm.com/docs/git-notes).

The format written is the one defined by the
[git-appraise code review system](https://github.com/google/git-appraise), so pull
requests that are mirrored using this tool can be reviewed using git-appraise.

## Disclaimer

This is not an officially supported Google product.

## Organization

There are 3 packages in this repo:
- `mirror` is a go library for mirroring the pull request metadata into git-notes.
- `batch` is a batch processor to mirror Github data into a local repository.
- `app` is a webapp/bot that sets up Github webhooks and mirrors data incrementally
  whenever an interesting event happens on the Github repo.

### The Batch Tool

The batch tool performs a single pass of reading all of the pull request metadata for
a repo, and mirroring it into your local clone of that repo.

The tool can support running unauthenticated, but will be extremely rate-limited, so
it is better if you create a [personal access token](https://help.github.com/articles/creating-an-access-token-for-command-line-use/),
with the `repo` scope, for it to use.

Setup:

```shell
go get github.com/google/git-pull-request-mirror/batch
go build -o ~/bin/pr-mirror "${GOPATH}/src/github.com/google/git-pull-request-mirror/batch/batch.go"
```

Example Usage (after you've cloned the repo to mirror):

```shell
git fetch origin '+refs/pull/*:refs/pull/*'
git appraise pull
~/bin/pr-mirror --target ${GITHUB_USER}/${GITHUB_REPO} --local ./ -auth-token ${YOUR_AUTH_TOKEN}
git appraise pull
git appraise push
```

### The Github Mirror App

This app allows users to continually update their git repositories with github
metadata (pull requests and build statuses). It runs in an AppEngine app, and
should expose a web interface at <PROJECT>.appspot.com.

It uses the app engine datastore to store its configuration.

To deploy:

```shell
gcloud app deploy ./app/admin/*.yaml
gcloud app deploy ./app/hooks/*.yaml
gcloud app deploy ./app/*.yaml
```
