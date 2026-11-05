package update

import (
	"encoding/hex"

	"crypto/ed25519"
)

// pubHex is the raw ed25519 public key that signs kern-update.json.
// The private key is not in this repo. It lives in the operator's key
// file and in the GitHub secret KERN_UPDATE_SIGNING_KEY.
//
// ponytail: one compiled-in key, ship a build that trusts the next key
// before signing with it.
const pubHex = "d2548cbeeddd6e5b6ca29e21edc061186606a61e54772140270c3405a1c25349"

func publicKey() ed25519.PublicKey {
	b, err := hex.DecodeString(pubHex)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("kern update public key is invalid")
	}
	return b
}
