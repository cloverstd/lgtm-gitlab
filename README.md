# lgtm-gitlab

lgtm-gitlab is used to auto merge gitlab CE MR with your LGTM like [gitlab EE approve](https://about.gitlab.com/2015/06/16/feature-highlight-approve-merge-request/)

# Usage

## Access token

You should create a access token on your gitlab.

## Reviewers List (optional)

You can define a list of gitlab users who will have permission to do LGTM. Add your reviewers in [reviewers.yaml](https://github.com/cloverstd/lgtm-gitlab/blob/master/reviewers.yaml)

## Run lgtm-gitlab

### Binary
```shell
./lgtm --help
  -db_path string
    	bolt db data (default "lgtm.data")
  -gitlab_url string
    	e.g. https://your.gitlab.com
  -lgtm_count int
    	lgtm user count (default 2)
  -lgtm_note string
    	lgtm note (default "LGTM")
  -log_level string
    	log level (default "info")
  -port int
    	http listen port (default 8989)
  -token string
    	gitlab private token which used to accept merge request. can be found in https://your.gitlab.com/profile/account
```

### Docker

```shell
docker run -d --restart=always \
    --name lgtm-gitlab \
    -e LGTM_TOKEN=YOUR_TOKEN \
    -e LGTM_GITLAB_URL=http://your_gitlab_url \
    -p 8989:8989 \
    cloverstd/lgtm-gitlab
```

## Comment LGTM on MR

Now you can comment a LGTM on gitlab MR, when the `lgtm_count` achieve, the MR will be merged.