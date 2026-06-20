// Package diff acquires git diffs across modes and parses them into per-file Diff structs.
package diff

// Mode selects how a diff is acquired and which revision supplies new content.
type Mode int

const (
	ModeStaged Mode = iota // staged changes vs HEAD; new content from the index
	ModeRange              // merge-base(from,to)..to; new content at <to>
	ModeCommit             // single commit vs its parent; new content at <commit>
)

// Diff is a single file change with its new-file content read from the
// mode-correct revision.
type Diff struct {
	OldPath        string `json:"old_path"`
	NewPath        string `json:"new_path"`
	Diff           string `json:"diff"`
	NewFileContent string `json:"new_file_content"`
	Ref            string `json:"ref"` // revision NewFileContent was read at; "" means the staged index

	IsBinary   bool  `json:"is_binary"`
	IsNew      bool  `json:"is_new"`
	IsDeleted  bool  `json:"is_deleted"`
	IsRenamed  bool  `json:"is_renamed"`
	Insertions int64 `json:"insertions"`
	Deletions  int64 `json:"deletions"`
}
