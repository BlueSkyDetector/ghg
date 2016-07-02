package ghg

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/mholt/archiver"
	"github.com/mitchellh/ioprogress"
	"github.com/octokit/go-octokit/octokit"
	"github.com/pkg/errors"
)

func getOctCli(token string) *octokit.Client {
	var auth octokit.AuthMethod
	if token != "" {
		auth = octokit.TokenAuth{AccessToken: token}
	}
	return octokit.NewClient(auth)
}

type ghg struct {
	binDir string
	target string
	client *octokit.Client
}

func (gh *ghg) getBinDir() string {
	if gh.binDir != "" {
		return gh.binDir
	}
	return "."
}

var releaseByTagURL = octokit.Hyperlink("repos/{owner}/{repo}/releases/tags/{tag}")
var archiveReg = regexp.MustCompile(`\.(?:zip|tgz|tar\.gz)$`)

func (gh *ghg) install() error {
	owner, repo, tag, err := getOwnerRepoAndTag(gh.target)
	if err != nil {
		return errors.Wrap(err, "failed to resolve target")
	}
	log.Printf("fetch the GitHub release for %s\n", gh.target)
	var url *url.URL
	if tag == "" {
		url, err = octokit.ReleasesLatestURL.Expand(octokit.M{"owner": owner, "repo": repo})
	} else {
		url, err = releaseByTagURL.Expand(octokit.M{"owner": owner, "repo": repo, "tag": tag})
	}
	if err != nil {
		return errors.Wrap(err, "failed to build GitHub URL")
	}
	release, r := gh.client.Releases(url).Latest()
	if r.HasError() {
		return errors.Wrap(r.Err, "failed to fetch latest release")
	}
	tag = release.TagName
	goarch := runtime.GOARCH
	goos := runtime.GOOS
	var urls []string
	for _, asset := range release.Assets {
		name := asset.Name
		if strings.Contains(name, goarch) && strings.Contains(name, goos) && archiveReg.MatchString(name) {
			urls = append(urls, fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s", owner, repo, tag, name))
		}
	}
	if len(urls) < 1 {
		return fmt.Errorf("no assets available")
	}
	log.Printf("install %s/%s version: %s", owner, repo, tag)
	for _, url := range urls {
		log.Printf("download %s\n", url)
		archivePath, err := download(url)
		if err != nil {
			return errors.Wrap(err, "failed to download")
		}
		workDir := filepath.Join(filepath.Dir(archivePath), "work")
		os.MkdirAll(workDir, 0755)
		log.Printf("extract %s\n", path.Base(url))
		err = extract(archivePath, workDir)
		if err != nil {
			return errors.Wrap(err, "failed to extract")
		}
		err = pickupExecutable(workDir, gh.getBinDir())
		if err != nil {
			return errors.Wrap(err, "failed to pickup")
		}
	}
	return nil
}

func download(url string) (fpath string, err error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		err = errors.Wrap(err, "failed to create request")
		return
	}
	req.Header.Set("User-Agent", fmt.Sprintf("ghg/%s", version))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		err = errors.Wrap(err, "failed to create request")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		err = fmt.Errorf("http response not OK. code: %d, url: %s", resp.StatusCode, url)
		return
	}
	archiveBase := path.Base(url)
	tempdir, err := ioutil.TempDir("", "ghg-")
	if err != nil {
		err = errors.Wrap(err, "failed to create tempdir")
		return
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tempdir)
		}
	}()
	fpath = filepath.Join(tempdir, archiveBase)
	f, err := os.Create(filepath.Join(tempdir, archiveBase))
	if err != nil {
		err = errors.Wrap(err, "failed to open file")
		return
	}
	defer f.Close()
	progressR := &ioprogress.Reader{
		Reader: resp.Body,
		Size:   resp.ContentLength,
	}
	_, err = io.Copy(f, progressR)
	if err != nil {
		err = errors.Wrap(err, "failed to read response")
		return
	}
	return fpath, nil
}

func extract(src, dest string) error {
	base := filepath.Base(src)
	if strings.HasSuffix(base, ".zip") {
		return archiver.Unzip(src, dest)
	}
	if strings.HasSuffix(base, ".tar.gz") || strings.HasSuffix(base, ".tgz") {
		return archiver.UntarGz(src, dest)
	}
	return fmt.Errorf("failed to extract file: %s", src)
}

var targetReg = regexp.MustCompile(`^(?:([^/]+)/)?([^@]+)(?:@(.+))?$`)

func getOwnerRepoAndTag(target string) (owner, repo, tag string, err error) {
	matches := targetReg.FindStringSubmatch(target)
	if len(matches) != 4 {
		err = fmt.Errorf("failed to get owner, repo and tag")
		return
	}
	owner = matches[1]
	repo = matches[2]
	tag = matches[3]
	if owner == "" {
		owner = repo
	}
	return
}

var executableReg = regexp.MustCompile(`^[a-z][-_a-zA-Z0-9]+(?:\.exe)?$`)

func pickupExecutable(src, dest string) error {
	defer os.RemoveAll(src)
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if name := info.Name(); (info.Mode()&0111) != 0 && executableReg.MatchString(name) {
			log.Printf("install %s\n", name)
			return os.Rename(path, filepath.Join(dest, name))
		}
		return nil
	})
}
