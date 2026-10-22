// Package runnerrelease asks GitHub which of its Actions runners is the
// latest, for a daemon that has been told its own runner is too old.
package runnerrelease

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// releases is where GitHub says which runner is its latest.
var releases = "https://api.github.com/repos/actions/runner/releases/latest"

// Latest asks GitHub which runner release is its latest.
func Latest(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releases, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %d", response.StatusCode)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil {
		return "", err
	}
	return strings.TrimPrefix(release.Tag, "v"), nil
}

// Older reports whether version a comes before version b, as 2.337.0 does
// before 2.338.0. A version that does not read as one is not older.
func Older(a, b string) bool {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(left), len(right)) {
		x, y := 0, 0
		var err error
		if i < len(left) {
			if x, err = strconv.Atoi(left[i]); err != nil {
				return false
			}
		}
		if i < len(right) {
			if y, err = strconv.Atoi(right[i]); err != nil {
				return false
			}
		}
		if x != y {
			return x < y
		}
	}
	return false
}
