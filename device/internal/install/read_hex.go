package install

import "fmt"

// hexReadScript reads at most n bytes of a file as hex pairs with dd and hexdump, for a busybox whose head has no -c
// and whose od has no -A or -t; file is already quoted
func hexReadScript(file string, n int) string {
	return fmt.Sprintf(`[ -f %s ] || exit 3; dd if=%s bs=%d count=1 2>/dev/null | hexdump -v -e '1/1 "%%02x "'`, file, file, n)
}
