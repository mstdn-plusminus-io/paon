package api

import "testing"

const misskeyDeleteActorURI = "https://misskey.day/users/apj6ocu8vj"
const misskeyDeleteTargetURI = "https://misskey.day/notes/aq8vqcdovr"
const misskeyDeleteKeyID = misskeyDeleteActorURI + "#main-key"

const misskeyDeletePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA/GeUgHvcrXPPjB1zUvJT
zdfki9K4D9a2ikkKr7LijpIL9/nIyJto5clwmguCviHAIYpKYkwLvYiK1KIPp6Vn
fqL+/6sMulxMrVooUGWuKHdj8kRT8qM4I8TxrzrDfFLpPA7tQ80SdSR8GbC+N2Rl
BILzI0kvuIvbnHGskEeHYtP4OGk2KxE3BIIYPP/AlL0lXHC44Aga/+5Q3OVZZcgx
sdluP54ZneypWFX+2EHpWtZEYVGq//uCb9a/3vChmBekRZgvNs6aVXL9Fk3SVBPv
ksRzCPRmuB1EPGykC2bE5Jt/8E5N32/OibdHFQy/meGnpxpFIKaaZnYT81zLQsXc
7QIDAQAB
-----END PUBLIC KEY-----`

const misskeyDeleteBody = `{"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1",{"Key":"sec:Key","manuallyApprovesFollowers":"as:manuallyApprovesFollowers","sensitive":"as:sensitive","Hashtag":"as:Hashtag","quoteUrl":"as:quoteUrl","toot":"http://joinmastodon.org/ns#","Emoji":"toot:Emoji","featured":"toot:featured","discoverable":"toot:discoverable","schema":"http://schema.org#","PropertyValue":"schema:PropertyValue","value":"schema:value","misskey":"https://misskey-hub.net/ns#","_misskey_content":"misskey:_misskey_content","_misskey_quote":"misskey:_misskey_quote","_misskey_reaction":"misskey:_misskey_reaction","_misskey_votes":"misskey:_misskey_votes","_misskey_summary":"misskey:_misskey_summary","_misskey_followedMessage":"misskey:_misskey_followedMessage","_misskey_requireSigninToViewContents":"misskey:_misskey_requireSigninToViewContents","_misskey_makeNotesFollowersOnlyBefore":"misskey:_misskey_makeNotesFollowersOnlyBefore","_misskey_makeNotesHiddenBefore":"misskey:_misskey_makeNotesHiddenBefore","_misskey_license":"misskey:_misskey_license","freeText":{"@id":"misskey:freeText","@type":"schema:text"},"isCat":"misskey:isCat","vcard":"http://www.w3.org/2006/vcard/ns#"}],"type":"Delete","actor":"https://misskey.day/users/apj6ocu8vj","object":{"id":"https://misskey.day/notes/aq8vqcdovr","type":"Tombstone"},"published":"2026-08-23T05:08:55.088Z","id":"https://misskey.day/e73d5213-1c6d-4066-8a5f-1393e95e753a","to":["https://www.w3.org/ns/activitystreams#Public"],"signature":{"type":"RsaSignature2017","creator":"https://misskey.day/users/apj6ocu8vj#main-key","nonce":"1c0eb046696630fa7395029ff68f47f4","created":"2026-08-23T05:08:55.092Z","signatureValue":"ngYzCRIkDavu6bQjDfXUHfCA904heNSDIDDkEyqOwFwzBxP0/sC483KRBgWtwNhducBkvgE7B7EF09QfL2IyNozVBbzgXnBF8RE2GtcbfnJVu22rwHMIqcyk6AhMJx03nkd8Q5QkZ9RqUdR0dyfkcbA23bgAgE5cZpPHtTg4FlcakBvTLcouzXo8LUULTqlnKoh9UATnU0Bjc7y2AfaxqioDxspdaD3i4Yq5vHtIeZD//JXCC6ZyWEdXwDtTC4nUZ6JwUA0cZCOwoF9rYb54L1OmDc+t2gGz14mqLnNzvZxvUM+Xf7tjzx8PKdRYFrIKoD/4e3yF7lFxUaCsnhJR7w=="}}`

func TestMisskeyDeleteLinkedDataSignatureVerifiesAfterCompaction(t *testing.T) {
	verificationBody := activityPubCompactCollectionBody([]byte(misskeyDeleteBody))
	payload, err := parseActivityPayload(verificationBody)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Delete" || payload.Actor != misskeyDeleteActorURI || payload.Object.ID != misskeyDeleteTargetURI || payload.Signature.Creator != misskeyDeleteKeyID {
		t.Fatalf("Misskey Delete payload = %#v", payload)
	}
	publicKey, err := activityPublicKey(misskeyDeletePublicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyActivityPubLinkedDataSignature(verificationBody, publicKey) {
		t.Fatal("Misskey Delete linked-data signature did not survive compaction")
	}
}
