package credentialhelper

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jenkins-x/jx-helpers/v3/pkg/termcolor"
	"github.com/jenkins-x/jx-logging/v3/pkg/log"

	"github.com/jenkins-x/jx-helpers/v3/pkg/files"

	"github.com/jenkins-x/jx-helpers/v3/pkg/stringhelpers"
)

const (
	XDG_CONFIG_HOME         = "XDG_CONFIG_HOME"
	GIT_SECRET_MOUNT_PATH   = "GIT_SECRET_MOUNT_PATH"
	GIT_SECRET_KEY_USER     = "GIT_SECRET_KEY_USER"
	GIT_SECRET_KEY_PASSWORD = "GIT_SECRET_KEY_PASSWORD"
	GIT_SECRET_SERVER       = "GIT_SECRET_SERVER"
)

// GitCredential represents the different parts of a git credential URL
// See also https://git-scm.com/docs/git-credential
type GitCredential struct {
	Protocol string
	Host     string
	Path     string
	Username string
	Password string
}

// CreateGitCredential creates a CreateGitCredential instance from a slice of strings where each element is a key/value pair
// separated by '='.
func CreateGitCredential(lines []string) (GitCredential, error) {
	var credential GitCredential

	if lines == nil {
		return credential, errors.New("no data lines provided")
	}

	fieldMap, err := stringhelpers.ExtractKeyValuePairs(lines, "=")
	if err != nil {
		return credential, fmt.Errorf("unable to extract git credential parameters: %w", err)
	}

	data, err := json.Marshal(fieldMap)
	if err != nil {
		return GitCredential{}, fmt.Errorf("unable to marshal git credential data: %w", err)
	}

	err = json.Unmarshal(data, &credential)
	if err != nil {
		return GitCredential{}, fmt.Errorf("unable unmarshal git credential data: %w", err)
	}

	return credential, nil
}

// CreateGitCredentialFromURL creates a CreateGitCredential instance from a URL and optional username and password.
func CreateGitCredentialFromURL(gitURL string, username string, password string) (GitCredential, error) {
	var credential GitCredential

	if gitURL == "" {
		return credential, errors.New("url cannot be empty")
	}

	u, err := url.Parse(gitURL)
	if err != nil {
		return credential, fmt.Errorf("unable to parse URL %s: %w", gitURL, err)
	}

	credential.Protocol = u.Scheme
	credential.Host = u.Host
	credential.Path = u.Path
	user := u.User

	// default missing user/pwd from the URL
	if user != nil {
		if username == "" {
			username = user.Username()
		}
		if password == "" {
			password, _ = user.Password()
		}
	}
	if username != "" {
		credential.Username = username
	}
	if password != "" {
		credential.Password = password
	}
	return credential, nil
}

// String returns a string representation of this instance according to the expected format of git credential helpers.
// See also https://git-scm.com/docs/git-credential
func (g *GitCredential) String() string {
	answer := ""

	value := reflect.ValueOf(g).Elem()
	typeOfT := value.Type()

	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		answer = answer + fmt.Sprintf("%s=%v\n", strings.ToLower(typeOfT.Field(i).Name), field.Interface())
	}

	answer = answer + "\n"

	return answer
}

// Clone clones this GitCredential instance
func (g *GitCredential) Clone() GitCredential {
	clone := GitCredential{}

	value := reflect.ValueOf(g).Elem()
	typeOfT := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		value := field.String()
		v := reflect.ValueOf(&clone).Elem().FieldByName(typeOfT.Field(i).Name)
		v.SetString(value)
	}

	return clone
}

// URL returns a URL from the data of this instance. If not enough information exist an error is returned
func (g *GitCredential) URL() (url.URL, error) {
	urlAsString := g.Protocol + "://" + g.Host
	if g.Path != "" {
		urlAsString = stringhelpers.UrlJoin(urlAsString, g.Path)
	}
	u, err := url.Parse(urlAsString)
	if err != nil {
		return url.URL{}, fmt.Errorf("unable to construct URL: %w", err)
	}

	u.User = url.UserPassword(g.Username, g.Password)
	return *u, nil
}

// WriteGitCredentialFromSecretMount writes mounted kubernetes secret credentials to file $XDG_CONFIG_HOME/git/credentials
// see for more details https://git-scm.com/docs/git-credential-store
func WriteGitCredentialFromSecretMount() error {
	xdgCongifHome := os.Getenv(XDG_CONFIG_HOME)
	mountLocation := os.Getenv(GIT_SECRET_MOUNT_PATH)
	userKey := os.Getenv(GIT_SECRET_KEY_USER)
	passKey := os.Getenv(GIT_SECRET_KEY_PASSWORD)

	server, err := parseGitSecretServerUrl(os.Getenv(GIT_SECRET_SERVER))
	if err != nil {
		return fmt.Errorf("failed to parse environment variable %s=%s: %w", GIT_SECRET_SERVER, os.Getenv(GIT_SECRET_SERVER), err)
	}

	if mountLocation == "" {
		return fmt.Errorf("no $%s environment variable set", GIT_SECRET_MOUNT_PATH)
	}

	if userKey == "" {
		userKey = "username"
	}

	if passKey == "" {
		passKey = "password"
	}

	exists, err := files.DirExists(mountLocation)
	if err != nil {
		return fmt.Errorf("failed to check if %s exists: %w", mountLocation, err)
	}
	if !exists {
		return fmt.Errorf("failed to find directory %s", mountLocation)
	}

	userPath := filepath.Join(mountLocation, userKey)
	passPath := filepath.Join(mountLocation, passKey)

	exists, err = files.FileExists(userPath)
	if err != nil {
		return fmt.Errorf("failed to check if %s exists: %w", userPath, err)
	}
	if !exists {
		return fmt.Errorf("failed to find user secret %s", userPath)
	}

	exists, err = files.FileExists(passPath)
	if err != nil {
		return fmt.Errorf("failed to check if %s exists: %w", passPath, err)
	}
	if !exists {
		return fmt.Errorf("failed to find password secret %s", passPath)
	}

	userData, err := os.ReadFile(userPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", userPath, err)
	}

	passData, err := os.ReadFile(passPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", passPath, err)
	}

	// match structure defined here https://git-scm.com/docs/git-credential-store
	file, err := getCredentialsFilename(xdgCongifHome)
	if err != nil {
		return fmt.Errorf("failed to get credentials filename to use to write git auth to: %w", err)
	}

	contents := fmt.Sprintf("%s://%s:%s@%s", server.Scheme, userData, passData, server.Host)

	err = os.WriteFile(file, []byte(contents), files.DefaultFileWritePermissions)
	if err != nil {
		return fmt.Errorf("failed to write file %s: %w", file, err)
	}

	log.Logger().Infof("wrote git credentials file %s", termcolor.ColorInfo(file))
	return nil
}

// match structure defined here https://git-scm.com/docs/git-credential-store
func getCredentialsFilename(xdgCongifHome string) (string, error) {
	if xdgCongifHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no $XDG_CONFIG_HOME set and failed to find user home directory: %w", err)
		}
		return filepath.Join(home, ".git-credentials"), nil
	}

	writeDirectory := filepath.Join(xdgCongifHome, "git")
	exists, err := files.DirExists(writeDirectory)
	if err != nil {
		return "", fmt.Errorf("failed to check if directory %s exists: %w", writeDirectory, err)
	}
	if !exists {
		err = os.MkdirAll(writeDirectory, os.ModePerm)
		if err != nil {
			return "", fmt.Errorf("failed to create directory %s: %w", writeDirectory, err)
		}
	}
	return filepath.Join(xdgCongifHome, "git", "credentials"), nil
}

func parseGitSecretServerUrl(s string) (*url.URL, error) {

	if s == "" {
		return url.Parse("https://github.com")
	}

	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return url.Parse(s)
	}

	return url.Parse("https://" + s)
}
