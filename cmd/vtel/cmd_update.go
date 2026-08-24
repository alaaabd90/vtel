package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func fetchRelease(url string) (*ghRelease, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github API %s: status %d: %s", url, resp.StatusCode, string(body))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func downloadAsset(url, dest string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}
	tmp := dest + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// assetNameForPlatform matches the release.yml build matrix - vtel
// currently only publishes linux amd64/arm64 server binaries.
func assetNameForPlatform() string {
	if runtime.GOARCH == "arm64" {
		return "vtel-linux-arm64"
	}
	return "vtel-linux-amd64"
}

func cmdUpdate() error {
	fmt.Println("  Fetching latest release...")
	rel, err := fetchRelease("https://api.github.com/repos/" + repoSlug + "/releases/latest")
	if err != nil {
		return fmt.Errorf("fetch release: %w", err)
	}
	fmt.Printf("  Latest: %s\n", rel.TagName)
	return installReleaseTag(rel)
}

func cmdRollback(args []string) error {
	if len(args) == 0 || args[0] == "" {
		return listReleasesForRollback()
	}
	tag := args[0]
	fmt.Printf("  Fetching release %s...\n", tag)
	rel, err := fetchRelease("https://api.github.com/repos/" + repoSlug + "/releases/tags/" + tag)
	if err != nil {
		return fmt.Errorf("fetch release %s: %w", tag, err)
	}
	return installReleaseTag(rel)
}

func installReleaseTag(rel *ghRelease) error {
	want := assetNameForPlatform()
	var url string
	for _, a := range rel.Assets {
		if a.Name == want {
			url = a.BrowserDownloadURL
			break
		}
	}
	if url == "" {
		return fmt.Errorf("%s not found in release %s", want, rel.TagName)
	}
	fmt.Printf("  Downloading %s/%s...\n", rel.TagName, want)
	if err := downloadAsset(url, binaryPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	fmt.Printf("  Installed %s (%s)\n", binaryPath, rel.TagName)

	if isServiceActive(serviceName) {
		fmt.Println("  Restarting service...")
		if err := exec.Command("systemctl", "restart", serviceName).Run(); err != nil {
			fmt.Printf("  Restart failed: %v\n", err)
		} else {
			fmt.Println("  Restarted.")
		}
	}
	return nil
}

func listReleasesForRollback() error {
	fmt.Println("  Usage: vtel rollback <tag>   (e.g. vtel rollback v1.0.0)")
	fmt.Println("  Fetching available releases...")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/" + repoSlug + "/releases")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return err
	}
	for _, r := range rels {
		fmt.Printf("    %s\n", r.TagName)
	}
	return nil
}
