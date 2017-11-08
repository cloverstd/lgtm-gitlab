package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"os"

	"strconv"

	"github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	yaml "gopkg.in/yaml.v2"
)

var (
	// ErrInvalidRequest ...
	ErrInvalidRequest = errors.New("invalid request body")
	// ErrInvalidContentType ...
	ErrInvalidContentType = errors.New("invalid content type")
	// RespOK ...
	RespOK       = []byte("OK")
	db           *bolt.DB
	buildVersion string
)

const (
	// ObjectNote ...
	ObjectNote = "note"
	// NoteableTypeMergeRequest ...
	NoteableTypeMergeRequest = "MergeRequest"
	// NoteLGTM ...
	NoteLGTM = "LGTM"
	// StatusCanbeMerged ...
	StatusCanbeMerged = "can_be_merged"
	bucketName        = "lgtm"
)

var (
	privateToken   = flag.String("token", "", "gitlab private token which used to accept merge request. can be found in https://your.gitlab.com/profile/account")
	gitlabURL      = flag.String("gitlab_url", "", "e.g. https://your.gitlab.com")
	validLGTMCount = flag.Int("lgtm_count", 2, "lgtm user count")
	lgtmNote       = flag.String("lgtm_note", NoteLGTM, "lgtm note")
	logLevel       = flag.String("log_level", "info", "log level")
	port           = flag.Int("port", 8989, "http listen port")
	dbPath         = flag.String("db_path", "lgtm.data", "bolt db data")
)

var (
	mutex sync.RWMutex
	// map[merge_request_id][count]
	lgtmCount = make(map[int]int)

	glURL *url.URL
)

// Conf configuration of allowed reviewers
type Conf struct {
	Reviewers []string `yaml:"reviewers"`
}

var reviewers Conf

// Get list of reviewers that has allowed to accept a merge request
func (c *Conf) getReviewers() *Conf {
	yamlFile, err := ioutil.ReadFile("reviewers.yaml")
	if err != nil {
		return nil
	}
	err = yaml.Unmarshal(yamlFile, c)
	if err != nil {
		logrus.Fatalf("Unmarshal: %v", err)
	}
	return c
}

func formatLogLevel(level string) logrus.Level {
	l, err := logrus.ParseLevel(string(level))
	if err != nil {
		l = logrus.InfoLevel
		logrus.Warnf("error parsing level %q: %v, using %q	", level, err, l)
	}

	return l
}

func init() {
	flag.Parse()
	logrus.SetOutput(os.Stderr)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
	logrus.SetLevel(formatLogLevel(*logLevel))
	logrus.WithField("buildVersion", buildVersion).Info("build info")
}

func main() {
	if *privateToken == "" {
		logrus.Fatal("private token is required")
	}
	if *gitlabURL == "" {
		logrus.Fatal("gitlab url is required")
	}

	var err error
	db, err = bolt.Open(*dbPath, 0600, nil)
	if err != nil {
		logrus.WithError(err).Fatal("open local db failed")
	}
	defer db.Close()
	parseURL(*gitlabURL)

	http.HandleFunc("/gitlab/hook", LGTMHandler)
	go func() {
		logrus.Infof("Webhook server listen on 0.0.0.0:%d", *port)
		http.ListenAndServe(":"+strconv.Itoa(*port), nil)
	}()

	<-(chan struct{})(nil)
}

func parseURL(urlStr string) {
	var err error
	glURL, err = url.Parse(urlStr)
	if err != nil {
		panic(err.Error())
	}
}

// LGTMHandler ...
func LGTMHandler(w http.ResponseWriter, r *http.Request) {
	logrus.WithFields(logrus.Fields{
		"method":      r.Method,
		"remote_addr": r.RemoteAddr,
	}).Infoln("access")
	var errRet error
	defer func() {
		if errRet != nil {
			errMsg := fmt.Sprintf("error occurs:%s", errRet.Error())
			logrus.WithError(errRet).Errorln("error response")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, errMsg)
			return
		}
		w.Write(RespOK)
	}()

	if r.Header.Get("Content-Type") != "application/json" {
		errRet = ErrInvalidContentType
		return
	}
	if r.Method != "POST" {
		errRet = ErrInvalidRequest
		return
	}
	if r.Body == nil {
		errRet = ErrInvalidRequest
		return
	}

	var comment Comment
	if err := json.NewDecoder(r.Body).Decode(&comment); err != nil {
		errRet = err
		return
	}

	go checkLgtm(comment)
}

func checkLgtm(comment Comment) error {
	if comment.ObjectKind != ObjectNote {
		// unmatched, do nothing
		return nil
	}

	if !checkReviewers(comment) {
		// unmatched, do nothing
		return nil
	}

	if comment.ObjectAttributes.NoteableType != NoteableTypeMergeRequest {
		// unmatched, do nothing
		return nil
	}

	if strings.ToUpper(comment.ObjectAttributes.Note) != *lgtmNote {
		// unmatched, do nothing
		return nil
	}

	// TODO: Check the comments LGTM two people are different people
	var (
		canbeMerged bool
		err         error
	)
	logrus.WithFields(logrus.Fields{
		"user": comment.User.Username,
		"note": comment.ObjectAttributes.Note,
		"MR":   comment.MergeRequest.Iid,
	}).Info("comment")

	canbeMerged, err = checkLGTMCount(comment)

	if err != nil {
		logrus.WithError(err).Errorln("check LGTM count failed")
		return nil
	}

	if canbeMerged && comment.MergeRequest.MergeStatus == StatusCanbeMerged {
		logrus.WithField("MR", comment.MergeRequest.Iid).Info("The MR can be merged.")
		acceptMergeRequest(comment.ProjectID, comment.MergeRequest.Iid, comment.MergeRequest.MergeParams.ForceRemoveSourceBranch)
	} else {
		logrus.WithFields(logrus.Fields{
			"MR":          comment.MergeRequest.Iid,
			"canbeMerged": canbeMerged,
			"MergeStatus": comment.MergeRequest.MergeStatus,
		}).Info("The MR can not be merged.")
	}
	return nil
}

// Check if users that send LGTM message has permission to do it.
func checkReviewers(comment Comment) bool {
	// If not exist a reviewers list or is empty, do nothing
	if reviewers.getReviewers() == nil || len(reviewers.Reviewers) == 0 {
		return true
	}
	// Check if the user is a reviewer
	for i := range reviewers.Reviewers {
		if reviewers.Reviewers[i] == comment.User.Username {
			return true
		}
	}
	logrus.Warn("User ", comment.User.Username, " is not allowed to do LGTM")
	return false
}

func checkLGTMCount(comment Comment) (bool, error) {
	mutex.Lock()
	defer mutex.Unlock()

	tx, err := db.Begin(true)
	if err != nil {
		return false, err
	}
	bucket, err := tx.CreateBucketIfNotExists([]byte(bucketName))
	if err != nil {
		return false, err
	}
	count := 0
	countKey := []byte(strconv.Itoa(comment.MergeRequest.Iid))
	countByte := bucket.Get(countKey)
	if len(countByte) > 0 {
		count, err = strconv.Atoi(string(countByte))
		if err != nil {
			logrus.WithField("value", string(countByte)).Warnln("wrong count")
			count = 0
			err = nil
		}
	}

	count++

	if err := bucket.Put(countKey, []byte(strconv.Itoa(count))); err != nil {
		return false, err
	}
	checkStatus := count%(*validLGTMCount) == 0

	if err := tx.Commit(); err != nil {
		return checkStatus, err
	}
	logrus.WithFields(logrus.Fields{
		"count": count,
		"MR":    comment.MergeRequest.Iid,
	}).Info("MR count")
	return checkStatus, nil
}

func acceptMergeRequest(projectID int, mergeRequestIID int, shouldRemoveSourceBranch string) {
	params := map[string]string{
		"should_remove_source_branch": shouldRemoveSourceBranch,
	}
	bodyBytes, err := json.Marshal(params)
	if err != nil {
		logrus.WithError(err).Errorln("json marshal failed")
		return
	}

	glURL.Path = fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/merge", projectID, mergeRequestIID)
	req, err := http.NewRequest("PUT", glURL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		logrus.WithError(err).Errorln("http NewRequest failed")
		return
	}
	req.Header.Set("Conntent-Type", "application/json")
	// authenticate
	req.Header.Set("PRIVATE-TOKEN", *privateToken) // my private token

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logrus.WithError(err).Errorln("execute request failed")
		return
	}

	switch resp.StatusCode {
	// 200
	case http.StatusOK:
		logrus.Info("accept merge request successfully")
	// 405
	case http.StatusMethodNotAllowed:
		logrus.Warnln("it has some conflicts and can not be merged")
	// 406
	case http.StatusNotAcceptable:
		logrus.Warnln("merge request is already merged or closed")
	default:
		logrus.WithFields(logrus.Fields{
			"http_code":   resp.StatusCode,
			"http_status": resp.Status,
		}).Errorln("accept merge failed")
	}
}

// Comment represents gitlab comment events
type Comment struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Name      string `json:"name"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`
	ProjectID int `json:"project_id"`
	Project   struct {
		Name              string      `json:"name"`
		Description       string      `json:"description"`
		WebURL            string      `json:"web_url"`
		AvatarURL         interface{} `json:"avatar_url"`
		GitSSHURL         string      `json:"git_ssh_url"`
		GitHTTPURL        string      `json:"git_http_url"`
		Namespace         string      `json:"namespace"`
		VisibilityLevel   int         `json:"visibility_level"`
		PathWithNamespace string      `json:"path_with_namespace"`
		DefaultBranch     string      `json:"default_branch"`
		Homepage          string      `json:"homepage"`
		URL               string      `json:"url"`
		SSHURL            string      `json:"ssh_url"`
		HTTPURL           string      `json:"http_url"`
	} `json:"project"`
	ObjectAttributes struct {
		ID                   int         `json:"id"`
		Note                 string      `json:"note"`
		NoteableType         string      `json:"noteable_type"`
		AuthorID             int         `json:"author_id"`
		CreatedAt            string      `json:"created_at"`
		UpdatedAt            string      `json:"updated_at"`
		ProjectID            int         `json:"project_id"`
		Attachment           interface{} `json:"attachment"`
		LineCode             interface{} `json:"line_code"`
		CommitID             string      `json:"commit_id"`
		NoteableID           int         `json:"noteable_id"`
		StDiff               interface{} `json:"st_diff"`
		System               bool        `json:"system"`
		UpdatedByID          interface{} `json:"updated_by_id"`
		Type                 interface{} `json:"type"`
		Position             interface{} `json:"position"`
		OriginalPosition     interface{} `json:"original_position"`
		ResolvedAt           interface{} `json:"resolved_at"`
		ResolvedByID         interface{} `json:"resolved_by_id"`
		DiscussionID         string      `json:"discussion_id"`
		OriginalDiscussionID interface{} `json:"original_discussion_id"`
		URL                  string      `json:"url"`
	} `json:"object_attributes"`
	Repository struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Description string `json:"description"`
		Homepage    string `json:"homepage"`
	} `json:"repository"`
	MergeRequest struct {
		ID              int         `json:"id"`
		TargetBranch    string      `json:"target_branch"`
		SourceBranch    string      `json:"source_branch"`
		SourceProjectID int         `json:"source_project_id"`
		AuthorID        int         `json:"author_id"`
		AssigneeID      int         `json:"assignee_id"`
		Title           string      `json:"title"`
		CreatedAt       string      `json:"created_at"`
		UpdatedAt       string      `json:"updated_at"`
		MilestoneID     interface{} `json:"milestone_id"`
		State           string      `json:"state"`
		MergeStatus     string      `json:"merge_status"`
		TargetProjectID int         `json:"target_project_id"`
		Iid             int         `json:"iid"`
		Description     string      `json:"description"`
		Position        int         `json:"position"`
		LockedAt        interface{} `json:"locked_at"`
		UpdatedByID     interface{} `json:"updated_by_id"`
		MergeError      interface{} `json:"merge_error"`
		MergeParams     struct {
			ForceRemoveSourceBranch string `json:"force_remove_source_branch"`
		} `json:"merge_params"`
		MergeWhenBuildSucceeds   bool        `json:"merge_when_build_succeeds"`
		MergeUserID              interface{} `json:"merge_user_id"`
		MergeCommitSha           interface{} `json:"merge_commit_sha"`
		DeletedAt                interface{} `json:"deleted_at"`
		InProgressMergeCommitSha interface{} `json:"in_progress_merge_commit_sha"`
		Source                   struct {
			Name              string `json:"name"`
			Description       string `json:"description"`
			WebURL            string `json:"web_url"`
			AvatarURL         string `json:"avatar_url"`
			GitSSHURL         string `json:"git_ssh_url"`
			GitHTTPURL        string `json:"git_http_url"`
			Namespace         string `json:"namespace"`
			VisibilityLevel   int    `json:"visibility_level"`
			PathWithNamespace string `json:"path_with_namespace"`
			DefaultBranch     string `json:"default_branch"`
			Homepage          string `json:"homepage"`
			URL               string `json:"url"`
			SSHURL            string `json:"ssh_url"`
			HTTPURL           string `json:"http_url"`
		} `json:"source"`
		Target struct {
			Name              string      `json:"name"`
			Description       string      `json:"description"`
			WebURL            string      `json:"web_url"`
			AvatarURL         interface{} `json:"avatar_url"`
			GitSSHURL         string      `json:"git_ssh_url"`
			GitHTTPURL        string      `json:"git_http_url"`
			Namespace         string      `json:"namespace"`
			VisibilityLevel   int         `json:"visibility_level"`
			PathWithNamespace string      `json:"path_with_namespace"`
			DefaultBranch     string      `json:"default_branch"`
			Homepage          string      `json:"homepage"`
			URL               string      `json:"url"`
			SSHURL            string      `json:"ssh_url"`
			HTTPURL           string      `json:"http_url"`
		} `json:"target"`
		LastCommit struct {
			ID        string    `json:"id"`
			Message   string    `json:"message"`
			Timestamp time.Time `json:"timestamp"`
			URL       string    `json:"url"`
			Author    struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
		} `json:"last_commit"`
		WorkInProgress bool `json:"work_in_progress"`
	} `json:"merge_request"`
}

// Follow-up support redis. HINCR lgtm merge_id 1
