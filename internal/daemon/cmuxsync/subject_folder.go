package cmuxsync

import "github.com/aloysbr/pigtail/pkg/pigtail"

// SubjectFolder is the folder the loop files a tab bound to subjectID in
// (D1): the subject's focus, else its theme's focus, else its effective space
// (its own when it names a known space of folders, else its theme's: boundOf,
// D3 of sub_x000000000000033), that folder when the pass has it, else
// « Inbox » (folderOf, spaceFolder).
// focus is the open focus (Deps.Focus), folders the loop's folder order alone
// (Deps.FolderOrder, never with the focus names: they would all be reserved
// and dropped). Standby is not decided here: a tab about to be born is in
// none. A read error is returned, never read as « Inbox ». pigtaild's launcher
// reads it to place a new tab at the bottom of that folder.
func SubjectFolder(reads folderReads, focus []pigtail.Focus, folders []string, subjectID string) (string, error) {
	b, err := boundOf(reads, subjectID, nil, knownSpaceIn(folders))
	if err != nil {
		return "", err
	}
	fv, _ := newFocusView(focus, reservedFolderNames(folders))
	return spaceFolder(fv.folderOf(b), fv.passFolders(folders)), nil
}
