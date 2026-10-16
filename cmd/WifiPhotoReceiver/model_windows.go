//go:build windows

package main

import (
	"sort"
	"strings"

	"github.com/lxn/walk"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/app"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/i18n"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/transfer"
)

// Filter values for the "Show" combo box.
func filterNames() []string {
	return []string{i18n.T("filter.all"), i18n.T("filter.missing"), i18n.T("filter.received"), i18n.T("filter.failed"), i18n.T("filter.excluded")}
}

var statusKeys = [...]string{"st.missing", "st.received", "st.queued", "st.downloading", "st.failed", "st.excluded"}

// kindText translates the file type names (JPEG and RAW stay as they are).
func kindText(k transfer.Kind) string {
	switch k {
	case transfer.KindVideo:
		return i18n.T("kind.video")
	case transfer.KindOther:
		return i18n.T("kind.other")
	}
	return k.String()
}

// languageNames returns the combo box entries: automatic, then each language.
func languageNames() []string {
	out := []string{i18n.T("set.langAuto")}
	for _, l := range i18n.Languages {
		out = append(out, l.Name)
	}
	return out
}

func languageCode(idx int) string {
	if idx <= 0 || idx > len(i18n.Languages) {
		return ""
	}
	return i18n.Languages[idx-1].Code
}

func languageIndex(code string) int {
	for i, l := range i18n.Languages {
		if l.Code == code {
			return i + 1
		}
	}
	return 0
}

func passesFilter(f int, it *transfer.Item) bool {
	switch f {
	case 1:
		return it.Status == transfer.StatusMissing || it.Status == transfer.StatusQueued || it.Status == transfer.StatusDownloading
	case 2:
		return it.Status == transfer.StatusReceived
	case 3:
		return it.Status == transfer.StatusFailed
	case 4:
		return it.Status == transfer.StatusExcluded
	}
	return true
}

// fileModel backs the file table.
type fileModel struct {
	walk.TableModelBase
	walk.SorterBase
	all     []transfer.Item
	rows    []transfer.Item
	filter  int
	sortCol int
	sortOrd walk.SortOrder
}

func newFileModel() *fileModel { return &fileModel{sortCol: 2} }

func (m *fileModel) RowCount() int { return len(m.rows) }

func (m *fileModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	it := &m.rows[row]
	switch col {
	case 0:
		return it.Name
	case 1:
		return kindText(it.Kind)
	case 2:
		if it.Captured.IsZero() {
			return ""
		}
		return it.Captured.Format("2006-01-02 15:04:05")
	case 3:
		return app.HumanBytes(it.Size)
	case 4:
		if it.Status == transfer.StatusDownloading && it.Size > 0 {
			return i18n.T("st.downloadingPct", int(it.Done*100/int64(it.Size)))
		}
		return it.Status.String()
	case 5:
		return it.Folder
	case 6:
		if it.Note != "" && it.LocalPath != "" {
			return it.LocalPath + "  (" + it.Note + ")"
		}
		if it.Note != "" {
			return it.Note
		}
		return it.LocalPath
	}
	return ""
}

func (m *fileModel) Sort(col int, order walk.SortOrder) error {
	m.sortCol, m.sortOrd = col, order
	m.applySort()
	return m.SorterBase.Sort(col, order)
}

func (m *fileModel) applySort() {
	less := func(a, b *transfer.Item) bool {
		switch m.sortCol {
		case 0:
			return strings.ToUpper(a.Name) < strings.ToUpper(b.Name)
		case 1:
			return a.Kind < b.Kind
		case 3:
			return a.Size < b.Size
		case 4:
			return a.Status < b.Status
		case 5:
			return a.Folder < b.Folder
		case 6:
			return a.LocalPath < b.LocalPath
		}
		if !a.Captured.Equal(b.Captured) {
			return a.Captured.Before(b.Captured)
		}
		return a.Name < b.Name
	}
	sort.SliceStable(m.rows, func(i, j int) bool {
		if m.sortOrd == walk.SortDescending {
			return less(&m.rows[j], &m.rows[i])
		}
		return less(&m.rows[i], &m.rows[j])
	})
}

// setItems updates the data. It returns true if the visible row set changed
// (requiring a full reset); otherwise changed rows are published one by one.
func (m *fileModel) setItems(items []transfer.Item) {
	m.all = items
	var rows []transfer.Item
	for i := range items {
		if passesFilter(m.filter, &items[i]) {
			rows = append(rows, items[i])
		}
	}
	old := m.rows
	m.rows = rows
	m.applySort()
	same := len(old) == len(m.rows)
	if same {
		for i := range old {
			if old[i].Key != m.rows[i].Key {
				same = false
				break
			}
		}
	}
	if !same {
		m.PublishRowsReset()
		return
	}
	for i := range old {
		a, b := &old[i], &m.rows[i]
		if a.Status != b.Status || a.Done != b.Done || a.Note != b.Note || a.LocalPath != b.LocalPath {
			m.PublishRowChanged(i)
		}
	}
}

func (m *fileModel) setFilter(f int) {
	m.filter = f
	m.rows = nil
	m.setItems(m.all)
	m.PublishRowsReset()
}

func (m *fileModel) keys(idx []int) []string {
	var out []string
	for _, i := range idx {
		if i >= 0 && i < len(m.rows) {
			out = append(out, m.rows[i].Key)
		}
	}
	return out
}

func (m *fileModel) indexesOf(keys map[string]bool) []int {
	var out []int
	for i := range m.rows {
		if keys[m.rows[i].Key] {
			out = append(out, i)
		}
	}
	return out
}

// statusOf is used for cell colouring.
func (m *fileModel) statusOf(row int) transfer.Status {
	if row < 0 || row >= len(m.rows) {
		return transfer.StatusMissing
	}
	return m.rows[row].Status
}
