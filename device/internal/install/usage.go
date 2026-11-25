package install

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// UsedKB runs du only for the space check of a board with a stage dir (space), so the boards without one never need
// it; du's own status, not awk's: a path that is there but cannot be read is an error, not a smaller answer
func (s *Shell) UsedKB(ctx context.Context, paths ...string) (int64, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	words := make([]string, len(paths))
	for i, p := range paths {
		words[i] = s.at(p)
	}
	script := fmt.Sprintf(`set --; for p in %s; do if [ -e "$p" ]; then set -- "$@" "$p"; fi; done; `+
		`[ $# -gt 0 ] || { echo 0; exit 0; }; o=$(du -sk "$@") || exit 2; printf '%%s\n' "$o" | awk '{ t += $1 } END { print t + 0 }'`,
		strings.Join(words, " "))
	res, err := s.run(ctx, script)
	if err != nil {
		return 0, err
	}
	kb, perr := strconv.ParseInt(strings.TrimSpace(string(res.Out)), 10, 64)
	if res.Code != 0 || perr != nil {
		return 0, fmt.Errorf("room taken by %s unknown (status %d)", strings.Join(paths, " "), res.Code)
	}
	return kb, nil
}
