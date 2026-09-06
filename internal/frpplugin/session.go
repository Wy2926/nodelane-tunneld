package frpplugin

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

const MetadataSessionID = "nodelane_session_id"

// BindSession is called only after the direct Login proof and store authorize
// the logical run. A fresh native ID isolates every stock-frps offline writer.
func BindSession(content LoginContent, logicalRunID string) (LoginContent, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return LoginContent{}, err
	}
	content.RunID = "fcs_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(entropy[:]))
	content.ClientID = logicalRunID
	content.Metas = map[string]string{
		MetadataRunID: logicalRunID, MetadataRunToken: content.Metas[MetadataRunToken], MetadataSessionID: content.RunID,
	}
	return content, nil
}

func ValidSessionID(id string) bool {
	if len(id) != 30 || !strings.HasPrefix(id, "fcs_") {
		return false
	}
	for _, character := range id[4:] {
		if (character < 'a' || character > 'z') && (character < '2' || character > '7') {
			return false
		}
	}
	return true
}

func ValidSessionUser(user UserInfo) bool {
	return user.User == "" && ValidSessionID(user.RunID) && len(user.Metas) == 3 &&
		user.Metas[MetadataSessionID] == user.RunID && user.Metas[MetadataRunID] != "" && user.Metas[MetadataRunToken] != ""
}
