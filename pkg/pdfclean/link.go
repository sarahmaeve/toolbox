package pdfclean

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ImageRef points to one extracted image referenced from a manifest.
// File is the basename within the manifest's output directory; Name
// is the source XObject identifier ("Im0", "Im0+Im1" for a stitched
// group, etc.) — used for diagnostics and consumed by LinkImages
// when building the markdown link target.
type ImageRef struct {
	File string
	Name string
}

// captionRules is the set of caption regexes pdfclean recognizes. Each pattern
// must use the ^ anchor; only matches at the start of a (post-cleaning) line
// count as a real caption, which keeps body references like "see Figure 4-2"
// from being misclassified.
//
// The single capture group is the figure label that ends up in the link's alt
// text and is preserved verbatim in the caption line below the image.
var captionRules = []*regexp.Regexp{
	// English: "Figure 4-2.", "Figure 1-1 —", "Figure 7-3:". The body may
	// continue on the same line; we only need to find the label.
	regexp.MustCompile(`^(Figure \d+-\d+)(?:[.\s—–:]|$)`),
	// Ukrainian / Russian: "Рисунок 1.2 –", "Рисунок 5", "Малюнок 4". Both
	// languages share the cyrillic Р here.
	regexp.MustCompile(`^(Рисунок \d+(?:\.\d+)?)(?:[.\s—–:]|$)`),
	// Ukrainian abbreviated forms — common in scientific texts: "Рис. 2.1.",
	// "Мал. 3", "Мал. 4.2".
	regexp.MustCompile(`^(Рис\. \d+(?:\.\d+)?)(?:[.\s—–:]|$)`),
	regexp.MustCompile(`^(Мал\. \d+(?:\.\d+)?)(?:[.\s—–:]|$)`),
	// "Схема №5", "Схема 12", "Схема 4.1".
	regexp.MustCompile(`^(Схема (?:№\s*)?\d+(?:\.\d+)?)(?:[.\s—–:]|$)`),
	// "Table 4-1" (used by ATP-style docs alongside figures).
	regexp.MustCompile(`^(Table \d+-\d+)(?:[.\s—–:]|$)`),
}

// pageMarkerRe matches the page markers clean() emits between pdfdump pages.
var pageMarkerRe = regexp.MustCompile(`^<!-- page (\d+) -->`)

// matchCaption returns the figure label if line is a caption, else "".
func matchCaption(line string) string {
	for _, re := range captionRules {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// LinkImages inserts Markdown image links above caption lines based
// on manifest. Page tracking comes from the <!-- page N --> markers
// Clean produces. Matching is page-level only: if a page has one
// caption and one image (or one stitched group), they're paired.
// Pages with images but no caption are left alone — we never invent
// a caption.
//
// When multiple captions appear on the same page, the manifest's
// images are consumed in stream order. When fewer images than
// captions are available, later captions are left unlinked.
//
// imgdir is the relative path prefix used in each generated Markdown
// link (e.g. "images/atp"). Joined to ImageRef.File via path.Join.
func LinkImages(markdown string, manifest map[int][]ImageRef, imgdir string) string {
	if len(manifest) == 0 {
		return markdown
	}

	var (
		out         strings.Builder
		currentPage = 1
		used        = map[int]int{} // page -> next-image index
	)
	out.Grow(len(markdown) + 256)

	for line := range strings.SplitSeq(markdown, "\n") {
		if m := pageMarkerRe.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				currentPage = n
			}
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}

		if label := matchCaption(line); label != "" {
			images := manifest[currentPage]
			idx := used[currentPage]
			if idx < len(images) {
				ref := images[idx]
				used[currentPage] = idx + 1
				out.WriteString("![")
				out.WriteString(label)
				out.WriteString("](")
				out.WriteString(path.Join(imgdir, ref.File))
				out.WriteString(")\n\n")
			}
		}

		out.WriteString(line)
		out.WriteByte('\n')
	}

	// strings.Split + "\n" rejoin adds one trailing \n; the input usually has
	// exactly one, so this matches. Trim only if we'd produced two.
	result := out.String()
	if strings.HasSuffix(markdown, "\n") && strings.HasSuffix(result, "\n\n") {
		result = result[:len(result)-1]
	} else if !strings.HasSuffix(markdown, "\n") && strings.HasSuffix(result, "\n") {
		result = result[:len(result)-1]
	}
	return result
}

// ParseManifest reads a pdf-image TSV manifest from r and indexes its
// rows by page number. Unknown columns are tolerated (header-driven
// lookup) so future manifest extensions don't break the linker.
func ParseManifest(r io.Reader) (map[int][]ImageRef, error) {
	cr := csv.NewReader(bufio.NewReader(r))
	cr.Comma = '\t'
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading manifest header: %w", err)
	}

	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, required := range []string{"file", "page", "name"} {
		if _, ok := col[required]; !ok {
			return nil, fmt.Errorf("manifest missing required column %q", required)
		}
	}

	out := map[int][]ImageRef{}
	for line := 2; ; line++ {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("manifest line %d: %w", line, err)
		}
		page, err := strconv.Atoi(row[col["page"]])
		if err != nil {
			return nil, fmt.Errorf("manifest line %d: invalid page %q: %w", line, row[col["page"]], err)
		}
		file := row[col["file"]]
		// Reject ".." traversal and absolute paths in the manifest; the
		// File field becomes part of a markdown link that downstream
		// renderers may resolve against the filesystem. filepath.IsLocal
		// is the canonical Go check for "stays within the rooted dir."
		if !filepath.IsLocal(file) {
			return nil, fmt.Errorf("manifest line %d: file %q is not a local path", line, file)
		}
		out[page] = append(out[page], ImageRef{
			File: file,
			Name: row[col["name"]],
		})
	}
	return out, nil
}

// LoadManifestFile opens a manifest TSV at path and returns the
// indexed result of ParseManifest. Convenience wrapper for callers
// that just want to point at a file.
func LoadManifestFile(path string) (map[int][]ImageRef, error) {
	f, err := os.Open(path) //nolint:gosec // G304: caller-supplied manifest path
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only file
	return ParseManifest(f)
}
