package api

import "testing"

const misskeySquareDeleteActorURI = "https://misskey-square.net/users/apwc34xd5r"
const misskeySquareDeleteTargetURI = "https://misskey-square.net/notes/araec1l37n"

const misskeySquareDeletePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEArSYG7dx86JQ5gleE9sle
7m8FcxPCjNX/6dhSqemdmoBLNre4YWpTEjbnN2czDJRJUAv6JJgm6WuqJDYmZ/uT
+IZU1sAlaTAZF4IaFn/FbEH/GOxn43fTT1sjTKPezTfXGKdeyoyxKxybF/hCNUcN
Ie2qO9YHnZgKtl6prm4ltcVZSl1PstC9RjZrf+LBO1Re9HojSQA3apkxpisl45+V
qoZn1RNu4zrydUIxXUxavDWG5xVVSRMWxRXFUeFpYzoWFM5u24DQoTpukYxzUCVZ
VUSuNAK9rZz7HSUA69NM+Kg3wko0bxh7xO8GTlKSXv6I7LGosPDDb4rpOYP8Z+cr
Kgwl6w8Owq15JJQBt0G3L3EP9Ezq8+9XwGnAEFB4JATh24X/ZW+fFRuiH7fAIKQV
ddumIP/lgSZTYezDTqzQtauCzaj52PsuEcAO2OcXsbmpeemb1fSwyLytkiPzhp7Y
7g81wCtQjB9A0e1EjPs6NyCaCyxznyMi40Zk0tjorfiOcF23i9bxCZQujxCSO5rH
05eSnTw7Y40JnIAm6Fwe3iRy3z5UvA8yxAKmBuTkh3Eal9Cohcs5vC5JDq2ySfSQ
RVjZQiiH1KKcFD5jlS+HzefR3ZyoIRW1VRY1/eZqFEkvvyZpFGoG9xCETAxlNdme
8ZwhXji2a5ej572w6vnJC/sCAwEAAQ==
-----END PUBLIC KEY-----`

const misskeySquareDeleteBody = `{"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1",{"Key":"sec:Key","manuallyApprovesFollowers":"as:manuallyApprovesFollowers","sensitive":"as:sensitive","Hashtag":"as:Hashtag","quoteUrl":"as:quoteUrl","toot":"http://joinmastodon.org/ns#","Emoji":"toot:Emoji","featured":"toot:featured","discoverable":"toot:discoverable","suspended":"toot:suspended","schema":"http://schema.org#","PropertyValue":"schema:PropertyValue","value":"schema:value","misskey":"https://misskey-hub.net/ns#","_misskey_content":"misskey:_misskey_content","_misskey_quote":"misskey:_misskey_quote","_misskey_reaction":"misskey:_misskey_reaction","_misskey_votes":"misskey:_misskey_votes","_misskey_summary":"misskey:_misskey_summary","_misskey_followedMessage":"misskey:_misskey_followedMessage","_misskey_requireSigninToViewContents":"misskey:_misskey_requireSigninToViewContents","_misskey_makeNotesFollowersOnlyBefore":"misskey:_misskey_makeNotesFollowersOnlyBefore","_misskey_makeNotesHiddenBefore":"misskey:_misskey_makeNotesHiddenBefore","_misskey_license":"misskey:_misskey_license","freeText":{"@id":"misskey:freeText","@type":"schema:text"},"isCat":"misskey:isCat","vcard":"http://www.w3.org/2006/vcard/ns#"}],"type":"Delete","actor":"https://misskey-square.net/users/apwc34xd5r","object":{"id":"https://misskey-square.net/notes/araec1l37n","type":"Tombstone"},"published":"2026-09-18T10:45:10.031Z","id":"https://misskey-square.net/10d0025f-d989-4322-968c-825b82eb7a6e","to":["https://www.w3.org/ns/activitystreams#Public"],"signature":{"type":"RsaSignature2017","creator":"https://misskey-square.net/users/apwc34xd5r#main-key","nonce":"11e72ae49739e331fb24a5462c916647","created":"2026-09-18T10:45:10.032Z","signatureValue":"osw0bpyjn7dzKdN3fw4YBAViOITggaGFsVCq39r1hhIfbKb5YB8yqFAGEvEykxMh3ekDd73um4VEjPz5O4P8zfBZPjV8mrX/XSZrfecuZgF90q7vGLSsBOJ031J9qFOVuzF7gCNQ71edMARBawOXSsNpl1WYBpKpIwFB5zr7Q1PeMzL6KNOu9iHZLvd+TSwfEhquhRqm94dl3dheDkZGneu2WGTbutzgdQ4pS6216+nzcYx5BGyCZpK+RZ3nTYXXRuZvlQgXeiw2FdglOMVjbIIpd0aU0oRTugrd5tiSVR44Q6duVgyAf82bl0e6qHW7gUVSPUQmRHXKdr/OGhvDOlwUK1+b9TlcgeZsD4Nw5DHMoQZe5H4jNwUOEcApazPSz37nRI7AL0CRzm8E92Y7chE02ArKBywHHndeZbsbiPFLMUpg/7/2Lo1a8jlfOFAK+lxPe/Blm6pR4XhU0RUMftusgqgh8GyCfRW6hrwWawF5pV7QaI06d8yws+S6j6LdF+XBeVI33EgK5DfHasFvwyGo2S9Wj1UCmiv9Q7U+YgxRnhlA51LDE1inMo4LZC+BJyR+PAx1GkAia7tQZve/XhEdmZsVkx4ZyYYquHMrZEVlwMZRPodmbM1p+CHA7u2Vgon+oMlJXqgci+j8qlzm5LiTcqV70Dgz0yaOxGFHReU="}}`

func TestMisskeySquareDeleteLinkedDataSignatureVerifies(t *testing.T) {
	publicKey, err := activityPublicKey(misskeySquareDeletePublicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		body []byte
	}{
		{name: "original", body: []byte(misskeySquareDeleteBody)},
		{name: "compacted", body: activityPubCompactCollectionBody([]byte(misskeySquareDeleteBody))},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := parseActivityPayload(test.body)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Type != "Delete" || payload.Actor != misskeySquareDeleteActorURI || payload.Object.ID != misskeySquareDeleteTargetURI || payload.Signature.Creator != misskeySquareDeleteActorURI+"#main-key" {
				t.Fatalf("Misskey Square Delete payload = %#v", payload)
			}
			if !verifyActivityPubLinkedDataSignature(test.body, publicKey) {
				t.Fatal("Misskey Square Delete linked-data signature did not verify")
			}
		})
	}
}
