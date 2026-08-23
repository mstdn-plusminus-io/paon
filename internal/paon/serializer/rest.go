package serializer

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"html"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

var hashtagPattern = regexp.MustCompile(`[#＃]([\pL\pN_·・‌]+)`)
var statusLinkPattern = regexp.MustCompile(`(?:https?|dat|dweb|ipfs|ipns|ssb|gopher|gemini)://[^\s<]+|xmpp:[^\s<]+|magnet:\?[^\s<]+|[#＃@][\pL\pN_@.\-·・‌]+`)
var previewCardOEmbedAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>` + "`" + `=]+))`)
var previewCardOEmbedHTMLTagPattern = regexp.MustCompile(`(?is)<\s*(/?)\s*([a-zA-Z0-9]+)\b([^>]*)>`)
var previewCardOEmbedScriptBlockPattern = regexp.MustCompile(`(?is)<\s*(script|style|noscript|template)\b[^>]*>.*?<\s*/\s*(script|style|noscript|template)\s*>`)

type SupportedLanguage struct {
	Code       string
	Name       string
	NativeName string
}

type Language struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type Application struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Website               *string  `json:"website"`
	Scopes                []string `json:"scopes"`
	RedirectURIs          []string `json:"redirect_uris"`
	RedirectURI           string   `json:"redirect_uri"`
	VapidKey              string   `json:"vapid_key"`
	ClientID              string   `json:"client_id,omitempty"`
	ClientSecret          string   `json:"client_secret,omitempty"`
	ClientSecretExpiresAt *int64   `json:"client_secret_expires_at,omitempty"`
}

type InstanceStats struct {
	DeliveryHistories []DeliveryHistory `json:"delivery_histories"`
}

type DeliveryHistory struct {
	Time         string `json:"time"`
	SuccessCount int64  `json:"success_count"`
	FailureCount int64  `json:"failure_count"`
}

var supportedLanguages = []SupportedLanguage{
	{Code: "aa", Name: "Afar", NativeName: "Afaraf"},
	{Code: "ab", Name: "Abkhaz", NativeName: "аҧсуа бызшә"},
	{Code: "ae", Name: "Avestan", NativeName: "avesta"},
	{Code: "af", Name: "Afrikaans", NativeName: "Afrikaans"},
	{Code: "ak", Name: "Akan", NativeName: "Akan"},
	{Code: "am", Name: "Amharic", NativeName: "አማርኛ"},
	{Code: "an", Name: "Aragonese", NativeName: "aragonés"},
	{Code: "ar", Name: "Arabic", NativeName: "اللغة العربية"},
	{Code: "as", Name: "Assamese", NativeName: "অসমীয়া"},
	{Code: "av", Name: "Avaric", NativeName: "авар мацӀ"},
	{Code: "ay", Name: "Aymara", NativeName: "aymar aru"},
	{Code: "az", Name: "Azerbaijani", NativeName: "azərbaycan dili"},
	{Code: "ba", Name: "Bashkir", NativeName: "башҡорт теле"},
	{Code: "be", Name: "Belarusian", NativeName: "беларуская мова"},
	{Code: "bg", Name: "Bulgarian", NativeName: "български език"},
	{Code: "bh", Name: "Bihari", NativeName: "भोजपुरी"},
	{Code: "bi", Name: "Bislama", NativeName: "Bislama"},
	{Code: "bm", Name: "Bambara", NativeName: "bamanankan"},
	{Code: "bn", Name: "Bengali", NativeName: "বাংলা"},
	{Code: "bo", Name: "Tibetan", NativeName: "བོད་ཡིག"},
	{Code: "br", Name: "Breton", NativeName: "brezhoneg"},
	{Code: "bs", Name: "Bosnian", NativeName: "bosanski jezik"},
	{Code: "ca", Name: "Catalan", NativeName: "Català"},
	{Code: "ce", Name: "Chechen", NativeName: "нохчийн мотт"},
	{Code: "ch", Name: "Chamorro", NativeName: "Chamoru"},
	{Code: "co", Name: "Corsican", NativeName: "corsu"},
	{Code: "cr", Name: "Cree", NativeName: "ᓀᐦᐃᔭᐍᐏᐣ"},
	{Code: "cs", Name: "Czech", NativeName: "čeština"},
	{Code: "cu", Name: "Old Church Slavonic", NativeName: "ѩзыкъ словѣньскъ"},
	{Code: "cv", Name: "Chuvash", NativeName: "чӑваш чӗлхи"},
	{Code: "cy", Name: "Welsh", NativeName: "Cymraeg"},
	{Code: "da", Name: "Danish", NativeName: "dansk"},
	{Code: "de", Name: "German", NativeName: "Deutsch"},
	{Code: "dv", Name: "Divehi", NativeName: "Dhivehi"},
	{Code: "dz", Name: "Dzongkha", NativeName: "རྫོང་ཁ"},
	{Code: "ee", Name: "Ewe", NativeName: "Eʋegbe"},
	{Code: "el", Name: "Greek", NativeName: "Ελληνικά"},
	{Code: "en", Name: "English", NativeName: "English"},
	{Code: "eo", Name: "Esperanto", NativeName: "Esperanto"},
	{Code: "es", Name: "Spanish", NativeName: "Español"},
	{Code: "et", Name: "Estonian", NativeName: "eesti"},
	{Code: "eu", Name: "Basque", NativeName: "euskara"},
	{Code: "fa", Name: "Persian", NativeName: "فارسی"},
	{Code: "ff", Name: "Fula", NativeName: "Fulfulde"},
	{Code: "fi", Name: "Finnish", NativeName: "suomi"},
	{Code: "fj", Name: "Fijian", NativeName: "Vakaviti"},
	{Code: "fo", Name: "Faroese", NativeName: "føroyskt"},
	{Code: "fr", Name: "French", NativeName: "Français"},
	{Code: "fy", Name: "Western Frisian", NativeName: "Frysk"},
	{Code: "ga", Name: "Irish", NativeName: "Gaeilge"},
	{Code: "gd", Name: "Scottish Gaelic", NativeName: "Gàidhlig"},
	{Code: "gl", Name: "Galician", NativeName: "galego"},
	{Code: "gu", Name: "Gujarati", NativeName: "ગુજરાતી"},
	{Code: "gv", Name: "Manx", NativeName: "Gaelg"},
	{Code: "ha", Name: "Hausa", NativeName: "هَوُسَ"},
	{Code: "he", Name: "Hebrew", NativeName: "עברית"},
	{Code: "hi", Name: "Hindi", NativeName: "हिन्दी"},
	{Code: "ho", Name: "Hiri Motu", NativeName: "Hiri Motu"},
	{Code: "hr", Name: "Croatian", NativeName: "Hrvatski"},
	{Code: "ht", Name: "Haitian", NativeName: "Kreyòl ayisyen"},
	{Code: "hu", Name: "Hungarian", NativeName: "magyar"},
	{Code: "hy", Name: "Armenian", NativeName: "Հայերեն"},
	{Code: "hz", Name: "Herero", NativeName: "Otjiherero"},
	{Code: "ia", Name: "Interlingua", NativeName: "Interlingua"},
	{Code: "id", Name: "Indonesian", NativeName: "Bahasa Indonesia"},
	{Code: "ie", Name: "Interlingue", NativeName: "Interlingue"},
	{Code: "ig", Name: "Igbo", NativeName: "Asụsụ Igbo"},
	{Code: "ii", Name: "Nuosu", NativeName: "ꆈꌠ꒿ Nuosuhxop"},
	{Code: "ik", Name: "Inupiaq", NativeName: "Iñupiaq"},
	{Code: "io", Name: "Ido", NativeName: "Ido"},
	{Code: "is", Name: "Icelandic", NativeName: "Íslenska"},
	{Code: "it", Name: "Italian", NativeName: "Italiano"},
	{Code: "iu", Name: "Inuktitut", NativeName: "ᐃᓄᒃᑎᑐᑦ"},
	{Code: "ja", Name: "Japanese", NativeName: "日本語"},
	{Code: "jv", Name: "Javanese", NativeName: "basa Jawa"},
	{Code: "ka", Name: "Georgian", NativeName: "ქართული"},
	{Code: "kg", Name: "Kongo", NativeName: "Kikongo"},
	{Code: "ki", Name: "Kikuyu", NativeName: "Gĩkũyũ"},
	{Code: "kj", Name: "Kwanyama", NativeName: "Kuanyama"},
	{Code: "kk", Name: "Kazakh", NativeName: "қазақ тілі"},
	{Code: "kl", Name: "Kalaallisut", NativeName: "kalaallisut"},
	{Code: "km", Name: "Khmer", NativeName: "ខេមរភាសា"},
	{Code: "kn", Name: "Kannada", NativeName: "ಕನ್ನಡ"},
	{Code: "ko", Name: "Korean", NativeName: "한국어"},
	{Code: "kr", Name: "Kanuri", NativeName: "Kanuri"},
	{Code: "ks", Name: "Kashmiri", NativeName: "कश्मीरी"},
	{Code: "ku", Name: "Kurmanji (Kurdish)", NativeName: "Kurmancî"},
	{Code: "kv", Name: "Komi", NativeName: "коми кыв"},
	{Code: "kw", Name: "Cornish", NativeName: "Kernewek"},
	{Code: "ky", Name: "Kyrgyz", NativeName: "Кыргызча"},
	{Code: "la", Name: "Latin", NativeName: "latine"},
	{Code: "lb", Name: "Luxembourgish", NativeName: "Lëtzebuergesch"},
	{Code: "lg", Name: "Ganda", NativeName: "Luganda"},
	{Code: "li", Name: "Limburgish", NativeName: "Limburgs"},
	{Code: "ln", Name: "Lingala", NativeName: "Lingála"},
	{Code: "lo", Name: "Lao", NativeName: "ລາວ"},
	{Code: "lt", Name: "Lithuanian", NativeName: "lietuvių kalba"},
	{Code: "lu", Name: "Luba-Katanga", NativeName: "Tshiluba"},
	{Code: "lv", Name: "Latvian", NativeName: "latviešu valoda"},
	{Code: "mg", Name: "Malagasy", NativeName: "fiteny malagasy"},
	{Code: "mh", Name: "Marshallese", NativeName: "Kajin M̧ajeļ"},
	{Code: "mi", Name: "Māori", NativeName: "te reo Māori"},
	{Code: "mk", Name: "Macedonian", NativeName: "македонски јазик"},
	{Code: "ml", Name: "Malayalam", NativeName: "മലയാളം"},
	{Code: "mn", Name: "Mongolian", NativeName: "Монгол хэл"},
	{Code: "mn-Mong", Name: "Traditional Mongolian", NativeName: "ᠮᠣᠩᠭᠣᠯ ᠬᠡᠯᠡ"},
	{Code: "mr", Name: "Marathi", NativeName: "मराठी"},
	{Code: "ms", Name: "Malay", NativeName: "Bahasa Melayu"},
	{Code: "mt", Name: "Maltese", NativeName: "Malti"},
	{Code: "my", Name: "Burmese", NativeName: "ဗမာစာ"},
	{Code: "na", Name: "Nauru", NativeName: "Ekakairũ Naoero"},
	{Code: "nb", Name: "Norwegian Bokmål", NativeName: "Norsk bokmål"},
	{Code: "nd", Name: "Northern Ndebele", NativeName: "isiNdebele"},
	{Code: "ne", Name: "Nepali", NativeName: "नेपाली"},
	{Code: "ng", Name: "Ndonga", NativeName: "Owambo"},
	{Code: "nl", Name: "Dutch", NativeName: "Nederlands"},
	{Code: "nn", Name: "Norwegian Nynorsk", NativeName: "Norsk Nynorsk"},
	{Code: "no", Name: "Norwegian", NativeName: "Norsk"},
	{Code: "nr", Name: "Southern Ndebele", NativeName: "isiNdebele"},
	{Code: "nv", Name: "Navajo", NativeName: "Diné bizaad"},
	{Code: "ny", Name: "Chichewa", NativeName: "chiCheŵa"},
	{Code: "oc", Name: "Occitan", NativeName: "occitan"},
	{Code: "oj", Name: "Ojibwe", NativeName: "ᐊᓂᔑᓈᐯᒧᐎᓐ"},
	{Code: "om", Name: "Oromo", NativeName: "Afaan Oromoo"},
	{Code: "or", Name: "Oriya", NativeName: "ଓଡ଼ିଆ"},
	{Code: "os", Name: "Ossetian", NativeName: "ирон æвзаг"},
	{Code: "pa", Name: "Panjabi", NativeName: "ਪੰਜਾਬੀ"},
	{Code: "pi", Name: "Pāli", NativeName: "पाऴि"},
	{Code: "pl", Name: "Polish", NativeName: "Polski"},
	{Code: "ps", Name: "Pashto", NativeName: "پښتو"},
	{Code: "pt", Name: "Portuguese", NativeName: "Português"},
	{Code: "qu", Name: "Quechua", NativeName: "Runa Simi"},
	{Code: "rm", Name: "Romansh", NativeName: "rumantsch grischun"},
	{Code: "rn", Name: "Kirundi", NativeName: "Ikirundi"},
	{Code: "ro", Name: "Romanian", NativeName: "Română"},
	{Code: "ru", Name: "Russian", NativeName: "Русский"},
	{Code: "rw", Name: "Kinyarwanda", NativeName: "Ikinyarwanda"},
	{Code: "sa", Name: "Sanskrit", NativeName: "संस्कृतम्"},
	{Code: "sc", Name: "Sardinian", NativeName: "sardu"},
	{Code: "sd", Name: "Sindhi", NativeName: "सिन्धी"},
	{Code: "se", Name: "Northern Sami", NativeName: "Davvisámegiella"},
	{Code: "sg", Name: "Sango", NativeName: "yângâ tî sängö"},
	{Code: "si", Name: "Sinhala", NativeName: "සිංහල"},
	{Code: "sk", Name: "Slovak", NativeName: "slovenčina"},
	{Code: "sl", Name: "Slovenian", NativeName: "slovenščina"},
	{Code: "sn", Name: "Shona", NativeName: "chiShona"},
	{Code: "so", Name: "Somali", NativeName: "Soomaaliga"},
	{Code: "sq", Name: "Albanian", NativeName: "Shqip"},
	{Code: "sr", Name: "Serbian", NativeName: "српски језик"},
	{Code: "ss", Name: "Swati", NativeName: "SiSwati"},
	{Code: "st", Name: "Southern Sotho", NativeName: "Sesotho"},
	{Code: "su", Name: "Sundanese", NativeName: "Basa Sunda"},
	{Code: "sv", Name: "Swedish", NativeName: "Svenska"},
	{Code: "sw", Name: "Swahili", NativeName: "Kiswahili"},
	{Code: "ta", Name: "Tamil", NativeName: "தமிழ்"},
	{Code: "te", Name: "Telugu", NativeName: "తెలుగు"},
	{Code: "tg", Name: "Tajik", NativeName: "тоҷикӣ"},
	{Code: "th", Name: "Thai", NativeName: "ไทย"},
	{Code: "ti", Name: "Tigrinya", NativeName: "ትግርኛ"},
	{Code: "tk", Name: "Turkmen", NativeName: "Türkmen"},
	{Code: "tl", Name: "Tagalog", NativeName: "Tagalog"},
	{Code: "tn", Name: "Tswana", NativeName: "Setswana"},
	{Code: "to", Name: "Tonga", NativeName: "faka Tonga"},
	{Code: "tr", Name: "Turkish", NativeName: "Türkçe"},
	{Code: "ts", Name: "Tsonga", NativeName: "itsonga"},
	{Code: "tt", Name: "Tatar", NativeName: "татар теле"},
	{Code: "tw", Name: "Twi", NativeName: "Twi"},
	{Code: "ty", Name: "Tahitian", NativeName: "Reo Tahiti"},
	{Code: "ug", Name: "Uyghur", NativeName: "ئۇيغۇرچە‎"},
	{Code: "uk", Name: "Ukrainian", NativeName: "Українська"},
	{Code: "ur", Name: "Urdu", NativeName: "اردو"},
	{Code: "uz", Name: "Uzbek", NativeName: "Ўзбек"},
	{Code: "ve", Name: "Venda", NativeName: "Tshivenḓa"},
	{Code: "vi", Name: "Vietnamese", NativeName: "Tiếng Việt"},
	{Code: "vo", Name: "Volapük", NativeName: "Volapük"},
	{Code: "wa", Name: "Walloon", NativeName: "walon"},
	{Code: "wo", Name: "Wolof", NativeName: "Wollof"},
	{Code: "xh", Name: "Xhosa", NativeName: "isiXhosa"},
	{Code: "yi", Name: "Yiddish", NativeName: "ייִדיש"},
	{Code: "yo", Name: "Yoruba", NativeName: "Yorùbá"},
	{Code: "za", Name: "Zhuang", NativeName: "Saɯ cueŋƅ"},
	{Code: "zh", Name: "Chinese", NativeName: "中文"},
	{Code: "zu", Name: "Zulu", NativeName: "isiZulu"},
	{Code: "zh-CN", Name: "Chinese (China)", NativeName: "简体中文"},
	{Code: "zh-HK", Name: "Chinese (Hong Kong)", NativeName: "繁體中文（香港）"},
	{Code: "zh-TW", Name: "Chinese (Taiwan)", NativeName: "繁體中文（臺灣）"},
	{Code: "zh-YUE", Name: "Cantonese", NativeName: "廣東話"},
	{Code: "ast", Name: "Asturian", NativeName: "Asturianu"},
	{Code: "chr", Name: "Cherokee", NativeName: "ᏣᎳᎩ ᎦᏬᏂᎯᏍᏗ"},
	{Code: "ckb", Name: "Sorani (Kurdish)", NativeName: "سۆرانی"},
	{Code: "cnr", Name: "Montenegrin", NativeName: "crnogorski"},
	{Code: "csb", Name: "Kashubian", NativeName: "Kaszëbsczi"},
	{Code: "gsw", Name: "Swiss German", NativeName: "Schwiizertütsch"},
	{Code: "jbo", Name: "Lojban", NativeName: "la .lojban."},
	{Code: "kab", Name: "Kabyle", NativeName: "Taqbaylit"},
	{Code: "ldn", Name: "Láadan", NativeName: "Láadan"},
	{Code: "lfn", Name: "Lingua Franca Nova", NativeName: "lingua franca nova"},
	{Code: "moh", Name: "Mohawk", NativeName: "Kanienʼkéha"},
	{Code: "ms-Arab", Name: "Jawi Malay", NativeName: "بهاس ملايو"},
	{Code: "nds", Name: "Low German", NativeName: "Plattdüütsch"},
	{Code: "pdc", Name: "Pennsylvania Dutch", NativeName: "Pennsilfaani-Deitsch"},
	{Code: "sco", Name: "Scots", NativeName: "Scots"},
	{Code: "sma", Name: "Southern Sami", NativeName: "Åarjelsaemien Gïele"},
	{Code: "smj", Name: "Lule Sami", NativeName: "Julevsámegiella"},
	{Code: "szl", Name: "Silesian", NativeName: "ślůnsko godka"},
	{Code: "tok", Name: "Toki Pona", NativeName: "toki pona"},
	{Code: "vai", Name: "Vai", NativeName: "ꕙꔤ"},
	{Code: "xal", Name: "Kalmyk", NativeName: "Хальмг келн"},
	{Code: "zba", Name: "Balaibalan", NativeName: "باليبلن"},
	{Code: "zgh", Name: "Standard Moroccan Tamazight", NativeName: "ⵜⴰⵎⴰⵣⵉⵖⵜ"},
}

func SupportedLanguages() []SupportedLanguage {
	out := make([]SupportedLanguage, len(supportedLanguages))
	copy(out, supportedLanguages)
	return out
}

func SupportedLanguageCodes() []string {
	out := make([]string, 0, len(supportedLanguages))
	for _, language := range supportedLanguages {
		out = append(out, language.Code)
	}
	return out
}

func SupportedLanguageRows() [][]string {
	out := make([][]string, 0, len(supportedLanguages))
	for _, language := range supportedLanguages {
		out = append(out, []string{language.Code, language.Name, language.NativeName})
	}
	return out
}

type Account struct {
	ID              string        `json:"id"`
	Username        string        `json:"username"`
	Acct            string        `json:"acct"`
	DisplayName     string        `json:"display_name"`
	Locked          bool          `json:"locked"`
	Bot             bool          `json:"bot"`
	Discoverable    *bool         `json:"discoverable"`
	Indexable       bool          `json:"indexable"`
	HideCollections *bool         `json:"hide_collections"`
	Group           bool          `json:"group"`
	CreatedAt       string        `json:"created_at"`
	Note            string        `json:"note"`
	URL             string        `json:"url"`
	URI             string        `json:"uri"`
	Avatar          string        `json:"avatar"`
	AvatarStatic    string        `json:"avatar_static"`
	Header          string        `json:"header"`
	HeaderStatic    string        `json:"header_static"`
	FollowersCount  int64         `json:"followers_count"`
	FollowingCount  int64         `json:"following_count"`
	StatusesCount   int64         `json:"statuses_count"`
	LastStatusAt    *string       `json:"last_status_at"`
	Moved           *Account      `json:"moved,omitempty"`
	Emojis          []CustomEmoji `json:"emojis"`
	Fields          []Field       `json:"fields"`
	Roles           []any         `json:"-"`
	Local           bool          `json:"-"`
	Suspended       *bool         `json:"suspended,omitempty"`
	Limited         *bool         `json:"limited,omitempty"`
	Memorial        *bool         `json:"memorial,omitempty"`
	NoIndex         *bool         `json:"noindex,omitempty"`
}

func (a Account) MarshalJSON() ([]byte, error) {
	type accountAlias Account
	out := struct {
		accountAlias
		Roles *[]any `json:"roles,omitempty"`
	}{
		accountAlias: accountAlias(a),
	}
	if a.Local {
		roles := a.Roles
		if roles == nil {
			roles = []any{}
		}
		out.Roles = &roles
	}
	return json.Marshal(out)
}

type AccountRole struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type CredentialAccount struct {
	Account
	Source CredentialSource `json:"source"`
	Role   any              `json:"role"`
}

func (a CredentialAccount) MarshalJSON() ([]byte, error) {
	return marshalAccountWithFields(a.Account, map[string]any{
		"source": a.Source,
		"role":   a.Role,
	})
}

type CredentialSource struct {
	Privacy             string        `json:"privacy"`
	Sensitive           bool          `json:"sensitive"`
	Language            *string       `json:"language"`
	QuotePolicy         string        `json:"quote_policy"`
	Note                string        `json:"note"`
	Fields              []SourceField `json:"fields"`
	FollowRequestsCount int64         `json:"follow_requests_count"`
	HideCollections     *bool         `json:"hide_collections"`
	Discoverable        *bool         `json:"discoverable"`
	Indexable           bool          `json:"indexable"`
	AttributionDomains  []string      `json:"attribution_domains"`
}

type SourceField struct {
	Name       string  `json:"name"`
	Value      string  `json:"value"`
	VerifiedAt *string `json:"verified_at"`
}

type MutedAccount struct {
	Account
	MuteExpiresAt *string `json:"mute_expires_at"`
}

func (a MutedAccount) MarshalJSON() ([]byte, error) {
	return marshalAccountWithFields(a.Account, map[string]any{
		"mute_expires_at": a.MuteExpiresAt,
	})
}

func marshalAccountWithFields(account Account, fields map[string]any) ([]byte, error) {
	return marshalJSONWithFields(account, fields)
}

func marshalJSONWithFields(value any, fields map[string]any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	for key, value := range fields {
		payload[key] = value
	}
	return json.Marshal(payload)
}

type Suggestion struct {
	Source  string   `json:"source"`
	Sources []string `json:"sources"`
	Account Account  `json:"account"`
}

type Field struct {
	Name       string  `json:"name"`
	Value      string  `json:"value"`
	VerifiedAt *string `json:"verified_at"`
}

type CustomEmoji struct {
	Shortcode       string `json:"shortcode"`
	URL             string `json:"url"`
	StaticURL       string `json:"static_url"`
	VisibleInPicker bool   `json:"visible_in_picker"`
	Category        any    `json:"category,omitempty"`
}

type Status struct {
	ID                 string              `json:"id"`
	CreatedAt          string              `json:"created_at"`
	InReplyToID        *string             `json:"in_reply_to_id"`
	InReplyToAccountID *string             `json:"in_reply_to_account_id"`
	Sensitive          bool                `json:"sensitive"`
	SpoilerText        string              `json:"spoiler_text"`
	Visibility         string              `json:"visibility"`
	Language           *string             `json:"language"`
	URI                string              `json:"uri"`
	URL                string              `json:"-"`
	URLNull            bool                `json:"-"`
	RepliesCount       int64               `json:"replies_count"`
	ReblogsCount       int64               `json:"reblogs_count"`
	FavouritesCount    int64               `json:"favourites_count"`
	QuotesCount        int64               `json:"quotes_count"`
	EditedAt           *string             `json:"edited_at"`
	Content            string              `json:"content"`
	Text               *string             `json:"text,omitempty"`
	Reblog             *Status             `json:"reblog"`
	Application        *StatusApplication  `json:"application,omitempty"`
	ApplicationPresent bool                `json:"-"`
	Account            Account             `json:"account"`
	MediaAttachments   []MediaAttachment   `json:"media_attachments"`
	Mentions           []Mention           `json:"mentions"`
	Tags               []Tag               `json:"tags"`
	Emojis             []CustomEmoji       `json:"emojis"`
	Card               any                 `json:"card"`
	Poll               any                 `json:"poll"`
	Quote              any                 `json:"quote"`
	QuoteApproval      StatusQuoteApproval `json:"quote_approval"`
	Favourited         *bool               `json:"favourited,omitempty"`
	Reblogged          *bool               `json:"reblogged,omitempty"`
	Muted              *bool               `json:"muted,omitempty"`
	Bookmarked         *bool               `json:"bookmarked,omitempty"`
	Pinned             *bool               `json:"pinned,omitempty"`
	Filtered           []any               `json:"-"`
	FilteredPresent    bool                `json:"-"`
}

// Quote is the full official Mastodon quote representation used on a top-level
// status and in edit history. QuotedStatus is deliberately a shallow status to
// keep quote chains finite.
type Quote struct {
	State        string  `json:"state"`
	QuotedStatus *Status `json:"quoted_status"`
}

// ShallowQuote is emitted when a status is itself nested inside another quote.
type ShallowQuote struct {
	State          string  `json:"state"`
	QuotedStatusID *string `json:"quoted_status_id"`
}

type StatusQuoteApproval struct {
	Automatic   []string `json:"automatic"`
	Manual      []string `json:"manual"`
	CurrentUser string   `json:"current_user"`
}

type StatusApplication struct {
	Name    string  `json:"name"`
	Website *string `json:"website"`
}

func (s Status) MarshalJSON() ([]byte, error) {
	type alias Status
	out := struct {
		alias
		Content  *string `json:"content,omitempty"`
		Filtered *[]any  `json:"filtered,omitempty"`
		URL      any     `json:"url"`
		App      *any    `json:"application,omitempty"`
	}{
		alias: alias(s),
	}
	if s.Text == nil {
		out.Content = &s.Content
	}
	if s.FilteredPresent || len(s.Filtered) > 0 {
		filtered := s.Filtered
		if filtered == nil {
			filtered = []any{}
		}
		out.Filtered = &filtered
	}
	if s.URLNull {
		out.URL = nil
	} else {
		out.URL = s.URL
	}
	if s.Application != nil {
		var app any = s.Application
		out.App = &app
	} else if s.ApplicationPresent {
		var app any
		out.App = &app
	}
	return json.Marshal(out)
}

type StatusSource struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	SpoilerText string `json:"spoiler_text"`
}

type Translation struct {
	DetectedSourceLanguage *string                      `json:"detected_source_language"`
	Language               string                       `json:"language"`
	Provider               *string                      `json:"provider"`
	SpoilerText            string                       `json:"spoiler_text"`
	Content                string                       `json:"content"`
	Poll                   *TranslationPoll             `json:"poll"`
	MediaAttachments       []TranslationMediaAttachment `json:"media_attachments"`
}

type TranslationPoll struct {
	ID      string              `json:"id"`
	Options []TranslationOption `json:"options"`
}

type TranslationOption struct {
	Title string `json:"title"`
}

type TranslationMediaAttachment struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type StatusEdit struct {
	Content          string            `json:"content"`
	SpoilerText      string            `json:"spoiler_text"`
	Sensitive        *bool             `json:"sensitive"`
	CreatedAt        string            `json:"created_at"`
	Account          *Account          `json:"account"`
	MediaAttachments []MediaAttachment `json:"media_attachments"`
	Emojis           []CustomEmoji     `json:"emojis"`
	Poll             *StatusEditPoll   `json:"poll,omitempty"`
	Quote            *Quote            `json:"quote,omitempty"`
}

type StatusEditPoll struct {
	Options []StatusEditPollOption `json:"options"`
}

type StatusEditPollOption struct {
	Title string `json:"title"`
}

type Context struct {
	Ancestors   []Status `json:"ancestors"`
	Descendants []Status `json:"descendants"`
}

type Poll struct {
	ID          string        `json:"id"`
	ExpiresAt   *string       `json:"expires_at"`
	Expired     bool          `json:"expired"`
	Multiple    bool          `json:"multiple"`
	VotesCount  int64         `json:"votes_count"`
	VotersCount *int64        `json:"voters_count"`
	Options     []PollOption  `json:"options"`
	Emojis      []CustomEmoji `json:"emojis"`
	Voted       *bool         `json:"voted,omitempty"`
	OwnVotes    *[]int        `json:"own_votes,omitempty"`
}

type PollOption struct {
	Title      string `json:"title"`
	VotesCount *int64 `json:"votes_count"`
}

type MediaAttachment struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	URL              string `json:"url"`
	PreviewURL       string `json:"preview_url"`
	RemoteURL        string `json:"remote_url"`
	PreviewRemoteURL string `json:"preview_remote_url"`
	TextURL          string `json:"text_url"`
	Meta             any    `json:"meta"`
	Description      string `json:"description"`
	Blurhash         string `json:"blurhash"`
}

func (m MediaAttachment) MarshalJSON() ([]byte, error) {
	type mediaAttachmentJSON struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		URL              any    `json:"url"`
		PreviewURL       any    `json:"preview_url"`
		RemoteURL        any    `json:"remote_url"`
		PreviewRemoteURL any    `json:"preview_remote_url"`
		TextURL          any    `json:"text_url"`
		Meta             any    `json:"meta"`
		Description      any    `json:"description"`
		Blurhash         any    `json:"blurhash"`
	}
	return json.Marshal(mediaAttachmentJSON{
		ID:               m.ID,
		Type:             m.Type,
		URL:              optionalStringAny(m.URL),
		PreviewURL:       optionalStringAny(m.PreviewURL),
		RemoteURL:        optionalStringAny(m.RemoteURL),
		PreviewRemoteURL: optionalStringAny(m.PreviewRemoteURL),
		TextURL:          optionalStringAny(m.TextURL),
		Meta:             m.Meta,
		Description:      optionalStringAny(m.Description),
		Blurhash:         optionalStringAny(m.Blurhash),
	})
}

type PreviewCard struct {
	URL              string              `json:"url"`
	Title            string              `json:"title"`
	Description      string              `json:"description"`
	Language         *string             `json:"language"`
	Type             string              `json:"type"`
	AuthorName       string              `json:"author_name"`
	AuthorURL        string              `json:"author_url"`
	ProviderName     string              `json:"provider_name"`
	ProviderURL      string              `json:"provider_url"`
	HTML             string              `json:"html"`
	Width            int                 `json:"width"`
	Height           int                 `json:"height"`
	Image            *string             `json:"image"`
	ImageDescription string              `json:"image_description"`
	EmbedURL         string              `json:"embed_url"`
	Blurhash         *string             `json:"blurhash"`
	PublishedAt      *string             `json:"published_at"`
	Authors          []PreviewCardAuthor `json:"authors"`
}

type PreviewCardAuthor struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Account *Account `json:"account"`
}

type PreviewCardTrendLink struct {
	PreviewCard
	History []any `json:"history"`
}

type AdminTrendLink struct {
	PreviewCardTrendLink
	ID             string `json:"id"`
	RequiresReview bool   `json:"requires_review"`
}

type AdminTrendStatus struct {
	Status
	RequiresReview bool `json:"requires_review"`
}

func (s AdminTrendStatus) MarshalJSON() ([]byte, error) {
	return marshalJSONWithFields(s.Status, map[string]any{
		"requires_review": s.RequiresReview,
	})
}

type AdminPreviewCardProvider struct {
	ID                string  `json:"id"`
	Domain            string  `json:"domain"`
	Trendable         *bool   `json:"trendable"`
	ReviewedAt        *string `json:"reviewed_at"`
	RequestedReviewAt *string `json:"requested_review_at"`
	RequiresReview    bool    `json:"requires_review"`
}

type ScheduledStatus struct {
	ID               string            `json:"id"`
	ScheduledAt      *string           `json:"scheduled_at"`
	Params           map[string]any    `json:"params"`
	MediaAttachments []MediaAttachment `json:"media_attachments"`
}

type Mention struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	URL      string `json:"url"`
	Acct     string `json:"acct"`
}

type Tag struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	History   []any  `json:"history"`
	Following *bool  `json:"following,omitempty"`
	Featuring *bool  `json:"featuring,omitempty"`
}

type TagDetail struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	History   []any  `json:"history"`
	Following *bool  `json:"following,omitempty"`
	Featuring *bool  `json:"featuring,omitempty"`
}

type Search struct {
	Accounts []Account   `json:"accounts"`
	Statuses []Status    `json:"statuses"`
	Hashtags []TagDetail `json:"hashtags"`
}

type AdminTag struct {
	TagDetail
	ID             string `json:"id"`
	Trendable      bool   `json:"trendable"`
	Usable         bool   `json:"usable"`
	RequiresReview bool   `json:"requires_review"`
	Listable       bool   `json:"listable"`
}

type Announcement struct {
	ID          string                `json:"id"`
	Content     string                `json:"content"`
	StartsAt    *string               `json:"starts_at"`
	EndsAt      *string               `json:"ends_at"`
	AllDay      bool                  `json:"all_day"`
	PublishedAt *string               `json:"published_at"`
	UpdatedAt   string                `json:"updated_at"`
	Read        *bool                 `json:"read,omitempty"`
	Mentions    []AnnouncementAccount `json:"mentions"`
	Statuses    []Status              `json:"statuses"`
	Tags        []Tag                 `json:"tags"`
	Emojis      []CustomEmoji         `json:"emojis"`
	Reactions   []Reaction            `json:"reactions"`
}

type AnnouncementAccount struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	URL      string `json:"url"`
	Acct     string `json:"acct"`
}

type AnnouncementStatus struct {
	ID      string `json:"id"`
	URL     string `json:"-"`
	URLNull bool   `json:"-"`
}

func (s AnnouncementStatus) MarshalJSON() ([]byte, error) {
	type announcementStatusJSON struct {
		ID  string `json:"id"`
		URL any    `json:"url"`
	}
	var statusURL any = s.URL
	if s.URLNull {
		statusURL = nil
	}
	return json.Marshal(announcementStatusJSON{ID: s.ID, URL: statusURL})
}

type Reaction struct {
	Name      string  `json:"name"`
	Count     int64   `json:"count"`
	Me        *bool   `json:"me,omitempty"`
	URL       *string `json:"url,omitempty"`
	StaticURL *string `json:"static_url,omitempty"`
}

type FeaturedTag struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	URL           string  `json:"url"`
	StatusesCount string  `json:"statuses_count"`
	LastStatusAt  *string `json:"last_status_at"`
}

type Notification struct {
	ID                string                             `json:"id"`
	Type              string                             `json:"type"`
	CreatedAt         string                             `json:"created_at"`
	GroupKey          string                             `json:"group_key"`
	Filtered          *bool                              `json:"filtered,omitempty"`
	Account           Account                            `json:"account"`
	Status            *Status                            `json:"status,omitempty"`
	Report            any                                `json:"report,omitempty"`
	Event             *AccountRelationshipSeveranceEvent `json:"event,omitempty"`
	ModerationWarning *AccountWarning                    `json:"moderation_warning,omitempty"`
}

type AccountRelationshipSeveranceEvent struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Purged         bool   `json:"purged"`
	TargetName     string `json:"target_name"`
	FollowersCount int    `json:"followers_count"`
	FollowingCount int    `json:"following_count"`
	CreatedAt      string `json:"created_at"`
}

type AccountWarning struct {
	ID            string   `json:"id"`
	Action        string   `json:"action"`
	Text          string   `json:"text"`
	StatusIDs     []string `json:"status_ids"`
	CreatedAt     string   `json:"created_at"`
	TargetAccount Account  `json:"target_account"`
	Appeal        any      `json:"appeal"`
}

type Report struct {
	ID            string   `json:"id"`
	ActionTaken   bool     `json:"action_taken"`
	ActionTakenAt *string  `json:"action_taken_at"`
	Category      string   `json:"category"`
	Comment       string   `json:"comment"`
	Forwarded     *bool    `json:"forwarded"`
	CreatedAt     string   `json:"created_at"`
	StatusIDs     []string `json:"status_ids"`
	RuleIDs       []string `json:"rule_ids"`
	TargetAccount Account  `json:"target_account"`
}

type AdminAccount struct {
	ID                     string           `json:"id"`
	Username               string           `json:"username"`
	Domain                 *string          `json:"domain"`
	CreatedAt              string           `json:"created_at"`
	Email                  *string          `json:"email"`
	IP                     *string          `json:"ip"`
	Confirmed              *bool            `json:"confirmed"`
	Suspended              bool             `json:"suspended"`
	Silenced               bool             `json:"silenced"`
	Sensitized             bool             `json:"sensitized"`
	Disabled               *bool            `json:"disabled"`
	Approved               *bool            `json:"approved"`
	Locale                 *string          `json:"locale"`
	InviteRequest          *string          `json:"invite_request"`
	CreatedByApplicationID *string          `json:"created_by_application_id,omitempty"`
	InvitedByAccountID     *string          `json:"invited_by_account_id,omitempty"`
	IPs                    []AdminAccountIP `json:"ips"`
	Account                Account          `json:"account"`
	Role                   any              `json:"role"`
}

type AdminAccountIP struct {
	IP     string  `json:"ip"`
	UsedAt *string `json:"used_at"`
}

type AdminAccountOptions struct {
	IPs                []AdminAccountIP
	Role               *models.UserRole
	EveryoneRole       *models.UserRole
	InviteRequest      *string
	InvitedByAccountID *string
}

type AdminReport struct {
	ID                   string        `json:"id"`
	ActionTaken          bool          `json:"action_taken"`
	ActionTakenAt        *string       `json:"action_taken_at"`
	Category             string        `json:"category"`
	Comment              string        `json:"comment"`
	Forwarded            *bool         `json:"forwarded"`
	CreatedAt            string        `json:"created_at"`
	UpdatedAt            string        `json:"updated_at"`
	Account              AdminAccount  `json:"account"`
	TargetAccount        AdminAccount  `json:"target_account"`
	AssignedAccount      *AdminAccount `json:"assigned_account"`
	ActionTakenByAccount *AdminAccount `json:"action_taken_by_account"`
	Statuses             []Status      `json:"statuses"`
	Rules                []any         `json:"rules"`
}

type AdminDomainAllow struct {
	ID        string `json:"id"`
	Domain    string `json:"domain"`
	CreatedAt string `json:"created_at"`
}

type AdminDomainBlock struct {
	ID             string  `json:"id"`
	Domain         string  `json:"domain"`
	Digest         string  `json:"digest"`
	CreatedAt      string  `json:"created_at"`
	Severity       string  `json:"severity"`
	RejectMedia    bool    `json:"reject_media"`
	RejectReports  bool    `json:"reject_reports"`
	PrivateComment *string `json:"private_comment"`
	PublicComment  *string `json:"public_comment"`
	Obfuscate      bool    `json:"obfuscate"`
}

type AdminExistingDomainBlockError struct {
	Error               string           `json:"error"`
	ExistingDomainBlock AdminDomainBlock `json:"existing_domain_block"`
}

type AdminEmailDomainBlock struct {
	ID                string                         `json:"id"`
	Domain            string                         `json:"domain"`
	CreatedAt         string                         `json:"created_at"`
	History           []AdminEmailDomainBlockHistory `json:"history"`
	AllowWithApproval bool                           `json:"allow_with_approval"`
}

type AdminEmailDomainBlockHistory struct {
	Day      string `json:"day"`
	Accounts string `json:"accounts"`
	Uses     string `json:"uses"`
}

type AdminCanonicalEmailBlock struct {
	ID                 string `json:"id"`
	CanonicalEmailHash string `json:"canonical_email_hash"`
}

type AdminMeasure struct {
	Key           string             `json:"key"`
	Unit          *string            `json:"unit"`
	Total         string             `json:"total"`
	HumanValue    *string            `json:"human_value,omitempty"`
	PreviousTotal *string            `json:"previous_total,omitempty"`
	Data          []AdminMeasureData `json:"data"`
}

type AdminMeasureData struct {
	Date  string `json:"date"`
	Value string `json:"value"`
}

type AdminDimension struct {
	Key  string               `json:"key"`
	Data []AdminDimensionData `json:"data"`
}

type AdminDimensionData struct {
	Key        string  `json:"key"`
	HumanKey   string  `json:"human_key"`
	Value      string  `json:"value"`
	Unit       *string `json:"unit,omitempty"`
	HumanValue *string `json:"human_value,omitempty"`
}

type AdminCohort struct {
	Period    string            `json:"period"`
	Frequency string            `json:"frequency"`
	Data      []AdminCohortData `json:"data"`
}

type AdminCohortData struct {
	Date  string  `json:"date"`
	Rate  float64 `json:"rate"`
	Value string  `json:"value"`
}

type AdminIPBlock struct {
	ID        string  `json:"id"`
	IP        string  `json:"ip"`
	Severity  string  `json:"severity"`
	Comment   string  `json:"comment"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
}

type AdminWebhookEvent struct {
	Event     string `json:"event"`
	CreatedAt string `json:"created_at"`
	Object    any    `json:"object"`
}

type Marker struct {
	LastReadID string `json:"last_read_id"`
	Version    int    `json:"version"`
	UpdatedAt  string `json:"updated_at"`
}

type Preferences struct {
	PostingDefaultVisibility  string `json:"posting:default:visibility"`
	PostingDefaultSensitive   bool   `json:"posting:default:sensitive"`
	PostingDefaultLanguage    string `json:"posting:default:language"`
	PostingDefaultQuotePolicy string `json:"posting:default:quote_policy"`
	ReadingExpandMedia        string `json:"reading:expand:media"`
	ReadingExpandSpoilers     bool   `json:"reading:expand:spoilers"`
	ReadingAutoplayGIFs       bool   `json:"reading:autoplay:gifs"`
}

type WebPushSubscription struct {
	ID        string         `json:"id"`
	Endpoint  string         `json:"endpoint"`
	Standard  bool           `json:"standard"`
	Alerts    map[string]any `json:"alerts"`
	ServerKey string         `json:"server_key"`
	Policy    string         `json:"policy"`
}

type Conversation struct {
	ID         string    `json:"id"`
	Unread     bool      `json:"unread"`
	Accounts   []Account `json:"accounts"`
	LastStatus *Status   `json:"last_status"`
}

type Relationship struct {
	ID                  string   `json:"id"`
	Following           bool     `json:"following"`
	ShowingReblogs      bool     `json:"showing_reblogs"`
	Notifying           bool     `json:"notifying"`
	Languages           []string `json:"languages"`
	FollowedBy          bool     `json:"followed_by"`
	Blocking            bool     `json:"blocking"`
	BlockedBy           bool     `json:"blocked_by"`
	Muting              bool     `json:"muting"`
	MutingNotifications bool     `json:"muting_notifications"`
	Requested           bool     `json:"requested"`
	RequestedBy         bool     `json:"requested_by"`
	DomainBlocking      bool     `json:"domain_blocking"`
	Endorsed            bool     `json:"endorsed"`
	Note                string   `json:"note"`
}

type FamiliarFollowers struct {
	ID       string    `json:"id"`
	Accounts []Account `json:"accounts"`
}

type List struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	RepliesPolicy string `json:"replies_policy"`
	Exclusive     bool   `json:"exclusive"`
}

type Filter struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Context      []string        `json:"context"`
	ExpiresAt    *string         `json:"expires_at"`
	FilterAction string          `json:"filter_action"`
	Keywords     []FilterKeyword `json:"-"`
	Statuses     []FilterStatus  `json:"-"`
	RulesPresent bool            `json:"-"`
}

func (f Filter) MarshalJSON() ([]byte, error) {
	type alias Filter
	out := struct {
		alias
		Keywords *[]FilterKeyword `json:"keywords,omitempty"`
		Statuses *[]FilterStatus  `json:"statuses,omitempty"`
	}{
		alias: alias(f),
	}
	if f.RulesPresent {
		keywords := f.Keywords
		if keywords == nil {
			keywords = []FilterKeyword{}
		}
		statuses := f.Statuses
		if statuses == nil {
			statuses = []FilterStatus{}
		}
		out.Keywords = &keywords
		out.Statuses = &statuses
	}
	return json.Marshal(out)
}

type V1Filter struct {
	ID           string   `json:"id"`
	Phrase       string   `json:"phrase"`
	Context      []string `json:"context"`
	WholeWord    bool     `json:"whole_word"`
	ExpiresAt    *string  `json:"expires_at"`
	Irreversible bool     `json:"irreversible"`
}

type FilterKeyword struct {
	ID        string `json:"id"`
	Keyword   string `json:"keyword"`
	WholeWord bool   `json:"whole_word"`
}

type FilterStatus struct {
	ID       string `json:"id"`
	StatusID string `json:"status_id"`
}

type FilterResult struct {
	Filter         any      `json:"filter"`
	KeywordMatches []string `json:"keyword_matches"`
	StatusMatches  any      `json:"status_matches"`
}

type Instance struct {
	Domain           string              `json:"domain"`
	Title            string              `json:"title"`
	Version          string              `json:"version"`
	ActualVersion    string              `json:"-"`
	SourceURL        string              `json:"source_url"`
	Description      string              `json:"description"`
	Usage            map[string]any      `json:"usage,omitempty"`
	Thumbnail        map[string]any      `json:"thumbnail,omitempty"`
	Icon             []map[string]string `json:"icon,omitempty"`
	Languages        []string            `json:"languages"`
	Configuration    map[string]any      `json:"configuration"`
	Registrations    map[string]any      `json:"registrations"`
	Contact          map[string]any      `json:"contact"`
	Rules            []any               `json:"rules"`
	APIVersions      map[string]int      `json:"api_versions,omitempty"`
	Stats            map[string]string   `json:"-"`
	URI              string              `json:"-"`
	Email            string              `json:"email,omitempty"`
	ShortDescription string              `json:"short_description,omitempty"`
	URLs             map[string]string   `json:"urls,omitempty"`
	ApprovalRequired *bool               `json:"approval_required,omitempty"`
	InvitesEnabled   *bool               `json:"invites_enabled,omitempty"`
}

type ExtendedDescription struct {
	UpdatedAt *string `json:"updated_at"`
	Content   string  `json:"content"`
}

type PrivacyPolicy struct {
	UpdatedAt *string `json:"updated_at"`
	Content   string  `json:"content"`
}

type TermsOfService struct {
	EffectiveDate string  `json:"effective_date"`
	Effective     bool    `json:"effective"`
	Content       string  `json:"content"`
	SucceededBy   *string `json:"succeeded_by"`
}

type InstanceDomainBlock struct {
	Domain   string  `json:"domain"`
	Digest   string  `json:"digest"`
	Severity string  `json:"severity"`
	Comment  *string `json:"comment"`
}

type InstanceRule struct {
	ID           string                             `json:"id"`
	Text         string                             `json:"text"`
	Hint         string                             `json:"hint"`
	Translations map[string]InstanceRuleTranslation `json:"translations"`
}

type InstanceRuleTranslation struct {
	Text string `json:"text"`
	Hint string `json:"hint"`
}

type InitialState struct {
	Meta                   map[string]any       `json:"meta"`
	Compose                map[string]any       `json:"compose"`
	Accounts               map[string]Account   `json:"accounts"`
	MediaAttachments       map[string]any       `json:"media_attachments"`
	Settings               map[string]any       `json:"settings"`
	Languages              [][]string           `json:"languages"`
	Features               []string             `json:"features"`
	PushSubscription       *WebPushSubscription `json:"push_subscription"`
	CriticalUpdatesPending *bool                `json:"critical_updates_pending,omitempty"`
	Role                   *Role                `json:"role"`
}

type InitialStateOptions struct {
	SiteTitle              string
	SiteTitleSet           bool
	ComposeText            string
	ComposeVisibility      string
	RegistrationsOpen      bool
	MascotURL              string
	AdminAccount           *models.Account
	OwnerAccount           *models.Account
	Role                   *models.UserRole
	EveryoneRole           *models.UserRole
	ServerSettings         *InitialStateServerSettings
	Settings               map[string]any
	User                   *models.User
	DisabledAccount        *models.Account
	MovedToAccount         *models.Account
	PushSubscription       *models.WebPushSubscription
	CriticalUpdatesPending *bool
	TermsOfServiceEnabled  bool
}

var mediaAttachmentFileExtensions = []string{
	".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".avif",
	".webm", ".mp4", ".m4v", ".mov",
	".ogg", ".oga", ".mp3", ".wav", ".flac", ".opus", ".aac", ".m4a", ".3gp", ".wma",
}

var mediaAttachmentMimeTypes = []string{
	"image/jpeg", "image/png", "image/gif", "image/heic", "image/heif", "image/webp", "image/avif",
	"video/webm", "video/mp4", "video/quicktime", "video/ogg",
	"audio/wave", "audio/wav", "audio/x-wav", "audio/x-pn-wave", "audio/vnd.wave", "audio/ogg", "audio/vorbis", "audio/mpeg", "audio/mp3", "audio/webm", "audio/flac", "audio/aac", "audio/m4a", "audio/x-m4a", "audio/mp4", "audio/3gpp", "video/x-ms-asf",
}

type Role struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Permissions string `json:"permissions"`
	Color       string `json:"color"`
	Highlighted bool   `json:"highlighted"`
}

type InitialStateServerSettings struct {
	ProfileDirectory      bool
	TrendsEnabled         bool
	TimelinePreview       bool
	ActivityAPIEnabled    bool
	TrendsAsLandingPage   bool
	LandingPage           string
	LocalLiveFeedAccess   string
	RemoteLiveFeedAccess  string
	LocalTopicFeedAccess  string
	RemoteTopicFeedAccess string
	StatusPageURL         string
	AutoPlayGIF           any
	DisplayMedia          any
	ReduceMotion          any
	UseBlurhash           any
	CropImages            any
}

func DefaultInitialStateServerSettings() InitialStateServerSettings {
	return InitialStateServerSettings{
		ProfileDirectory:      true,
		TrendsEnabled:         true,
		TimelinePreview:       true,
		ActivityAPIEnabled:    true,
		TrendsAsLandingPage:   true,
		LandingPage:           "trends",
		LocalLiveFeedAccess:   "public",
		RemoteLiveFeedAccess:  "public",
		LocalTopicFeedAccess:  "public",
		RemoteTopicFeedAccess: "public",
	}
}

func ListFromModel(list models.List) List {
	return List{
		ID:            strconv.FormatInt(list.ID, 10),
		Title:         list.Title,
		RepliesPolicy: repliesPolicyName(list.RepliesPolicy),
		Exclusive:     list.Exclusive,
	}
}

func FilterFromModel(filter models.CustomFilter, includeRules bool) Filter {
	out := Filter{
		ID:           strconv.FormatInt(filter.ID, 10),
		Title:        filter.Phrase,
		Context:      []string(filter.Context),
		ExpiresAt:    timePtr(filter.ExpiresAt),
		FilterAction: filterActionName(filter.Action),
	}
	if includeRules {
		out.Keywords = FilterKeywordsFromModel(filter.Keywords)
		out.Statuses = FilterStatusesFromModel(filter.Statuses)
		out.RulesPresent = true
	}
	return out
}

func FilterKeywordFromModel(keyword models.CustomFilterKeyword) FilterKeyword {
	return FilterKeyword{
		ID:        strconv.FormatInt(keyword.ID, 10),
		Keyword:   keyword.Keyword,
		WholeWord: keyword.WholeWord,
	}
}

func FilterKeywordsFromModel(keywords []models.CustomFilterKeyword) []FilterKeyword {
	out := make([]FilterKeyword, 0, len(keywords))
	for _, keyword := range keywords {
		out = append(out, FilterKeywordFromModel(keyword))
	}
	return out
}

func V1FilterFromKeyword(keyword models.CustomFilterKeyword) V1Filter {
	filter := keyword.CustomFilter
	return V1Filter{
		ID:           strconv.FormatInt(keyword.ID, 10),
		Phrase:       keyword.Keyword,
		Context:      []string(filter.Context),
		WholeWord:    keyword.WholeWord,
		ExpiresAt:    timePtr(filter.ExpiresAt),
		Irreversible: filter.Action == 1,
	}
}

func FilterStatusFromModel(status models.CustomFilterStatus) FilterStatus {
	return FilterStatus{
		ID:       strconv.FormatInt(status.ID, 10),
		StatusID: strconv.FormatInt(status.StatusID, 10),
	}
}

func FilterStatusesFromModel(statuses []models.CustomFilterStatus) []FilterStatus {
	out := make([]FilterStatus, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, FilterStatusFromModel(status))
	}
	return out
}

func MarkerFromModel(marker models.Marker) Marker {
	return Marker{
		LastReadID: strconv.FormatInt(marker.LastReadID, 10),
		Version:    marker.LockVersion,
		UpdatedAt:  restTimestamp(marker.UpdatedAt),
	}
}

func WebPushSubscriptionFromModel(cfg config.Config, subscription models.WebPushSubscription) WebPushSubscription {
	data := webPushData(subscription.Data)
	return WebPushSubscription{
		ID:        strconv.FormatInt(subscription.ID, 10),
		Endpoint:  subscription.Endpoint,
		Standard:  subscription.Standard,
		Alerts:    data.Alerts,
		ServerKey: cfg.VapidPublicKey,
		Policy:    firstNonEmptyString(data.Policy, "all"),
	}
}

type ReactionSource struct {
	Name      string
	Count     int64
	Me        bool
	URL       string
	StaticURL string
}

func AnnouncementFromModel(cfg config.Config, announcement models.Announcement, read *bool, statuses []models.Status, reactions []ReactionSource) Announcement {
	serializedStatuses := make([]Status, 0, len(statuses))
	for _, status := range statuses {
		serializedStatuses = append(serializedStatuses, StatusFromModel(cfg, status, nil))
	}
	return AnnouncementFromModelWithStatuses(cfg, announcement, read, serializedStatuses, reactions)
}

func AnnouncementFromModelWithStatuses(cfg config.Config, announcement models.Announcement, read *bool, statuses []Status, reactions []ReactionSource) Announcement {
	return Announcement{
		ID:          strconv.FormatInt(announcement.ID, 10),
		Content:     announcementContentHTML(cfg, announcement),
		StartsAt:    timePtr(announcement.StartsAt),
		EndsAt:      timePtr(announcement.EndsAt),
		AllDay:      announcement.AllDay,
		PublishedAt: timePtr(announcement.PublishedAt),
		UpdatedAt:   restTimestamp(announcement.UpdatedAt),
		Read:        read,
		Mentions:    announcementAccounts(cfg, announcement.MentionAccounts),
		Statuses:    statuses,
		Tags:        announcementTags(cfg, announcement.Text),
		Emojis:      customEmojis(cfg, announcement.CustomEmojis),
		Reactions:   reactionsFromSource(reactions, read != nil),
	}
}

func TagDetailFromModel(cfg config.Config, tag models.Tag, following *bool) TagDetail {
	return TagDetailFromModelWithRelationships(cfg, tag, following, nil, nil)
}

func TagDetailFromModelWithHistory(cfg config.Config, tag models.Tag, following *bool, history []any) TagDetail {
	return TagDetailFromModelWithRelationships(cfg, tag, following, nil, history)
}

func TagDetailFromModelWithRelationships(cfg config.Config, tag models.Tag, following *bool, featuring *bool, history []any) TagDetail {
	if history == nil {
		history = []any{}
	}
	return TagDetail{
		ID:        strconv.FormatInt(tag.ID, 10),
		Name:      tag.DisplayNameValue(),
		URL:       cfg.BaseURL() + "/tags/" + url.PathEscape(tag.Name),
		History:   history,
		Following: following,
		Featuring: featuring,
	}
}

func AdminTagFromModel(cfg config.Config, tag models.Tag) AdminTag {
	return AdminTagFromModelWithHistory(cfg, tag, nil)
}

func AdminTagFromModelWithHistory(cfg config.Config, tag models.Tag, history []any) AdminTag {
	return AdminTagFromModelWithHistoryAndTrendableDefault(cfg, tag, history, false)
}

func AdminTagFromModelWithHistoryAndTrendableDefault(cfg config.Config, tag models.Tag, history []any, trendableByDefault bool) AdminTag {
	return AdminTag{
		TagDetail:      TagDetailFromModelWithHistory(cfg, tag, nil, history),
		ID:             strconv.FormatInt(tag.ID, 10),
		Trendable:      nullBoolDefault(tag.Trendable, trendableByDefault),
		Usable:         nullBoolDefault(tag.Usable, true),
		RequiresReview: !tag.ReviewedAt.Valid,
		Listable:       nullBoolDefault(tag.Listable, true),
	}
}

func nullBoolDefault(value sql.NullBool, fallback bool) bool {
	if value.Valid {
		return value.Bool
	}
	return fallback
}

func CustomEmojiFromModel(cfg config.Config, emoji models.CustomEmoji) CustomEmoji {
	url := customEmojiURL(cfg, emoji, "original")
	staticURL := customEmojiURL(cfg, emoji, "static")
	out := CustomEmoji{
		Shortcode:       emoji.Shortcode,
		URL:             url,
		StaticURL:       staticURL,
		VisibleInPicker: emoji.VisibleInPicker,
	}
	if emoji.Category.ID != 0 {
		if emoji.Category.Name.Valid {
			category := emoji.Category.Name.String
			out.Category = &category
		} else {
			out.Category = (*string)(nil)
		}
	}
	return out
}

func customEmojis(cfg config.Config, emojis []models.CustomEmoji) []CustomEmoji {
	out := make([]CustomEmoji, 0, len(emojis))
	for _, emoji := range emojis {
		out = append(out, CustomEmojiFromModel(cfg, emoji))
	}
	return out
}

func FeaturedTagFromModel(cfg config.Config, featured models.FeaturedTag) FeaturedTag {
	return FeaturedTag{
		ID:            strconv.FormatInt(featured.ID, 10),
		Name:          featured.DisplayNameValue(),
		URL:           featuredTagURL(cfg, featured.Account, featured.Tag),
		StatusesCount: strconv.FormatInt(featured.StatusesCount, 10),
		LastStatusAt:  dateString(featured.LastStatusAt),
	}
}

func ExtendedDescriptionFromSetting(setting *models.Setting) ExtendedDescription {
	if setting == nil || !setting.Value.Valid || strings.TrimSpace(setting.Value.String) == "" {
		return ExtendedDescription{Content: ""}
	}
	return ExtendedDescription{
		UpdatedAt: timePtr(setting.UpdatedAt),
		Content:   simpleMarkdownHTMLWithOptions(setting.Value.String, simpleMarkdownOptions{EscapeHTML: false}),
	}
}

func PrivacyPolicyFromSetting(cfg config.Config, setting *models.Setting) PrivacyPolicy {
	value := defaultPrivacyPolicy
	updatedAt := sql.NullTime{Time: time.Date(2022, 10, 7, 0, 0, 0, 0, time.UTC), Valid: true}
	if setting != nil && setting.Value.Valid && strings.TrimSpace(setting.Value.String) != "" {
		value = setting.Value.String
		updatedAt = setting.UpdatedAt
	}
	value = strings.ReplaceAll(value, "%{domain}", cfg.LocalDomain)
	return PrivacyPolicy{
		UpdatedAt: timePtr(updatedAt),
		Content:   simpleMarkdownHTML(value),
	}
}

func TermsOfServiceFromModel(cfg config.Config, terms models.TermsOfService, succeededBy *models.TermsOfService, now time.Time) TermsOfService {
	effectiveDate := terms.PublishedAt.Time.Format(time.RFC3339)
	if terms.EffectiveDate.Valid {
		effectiveDate = terms.EffectiveDate.Time.Format("2006-01-02")
	}
	var successor *string
	if succeededBy != nil && succeededBy.EffectiveDate.Valid {
		value := succeededBy.EffectiveDate.Time.Format("2006-01-02")
		successor = &value
	}
	content := strings.ReplaceAll(terms.Text, "%{domain}", cfg.LocalDomain)
	return TermsOfService{
		EffectiveDate: effectiveDate,
		Effective:     terms.PublishedAt.Valid && terms.EffectiveDate.Valid && terms.EffectiveDate.Time.Before(startOfUTCDay(now)),
		Content:       simpleMarkdownHTML(content),
		SucceededBy:   successor,
	}
}

func startOfUTCDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func InstanceDomainBlockFromModel(block models.DomainBlock, withComment bool) InstanceDomainBlock {
	hash := sha256.Sum256([]byte(block.Domain))
	var comment *string
	if withComment && block.PublicComment.Valid {
		comment = &block.PublicComment.String
	}
	return InstanceDomainBlock{
		Domain:   obfuscateDomain(block.Domain, block.Obfuscate),
		Digest:   hex.EncodeToString(hash[:]),
		Severity: domainBlockSeverity(block.Severity),
		Comment:  comment,
	}
}

func ConversationFromModel(cfg config.Config, conversation models.AccountConversation, currentAccount *models.Account) Conversation {
	accounts := make([]Account, 0, len(conversation.ParticipantAccounts))
	for _, account := range conversation.ParticipantAccounts {
		accounts = append(accounts, AccountFromModel(cfg, account))
	}
	var lastStatus *Status
	if conversation.LastStatus != nil && conversation.LastStatus.ID != 0 {
		status := StatusFromModel(cfg, *conversation.LastStatus, currentAccount)
		lastStatus = &status
	}
	return Conversation{
		ID:         strconv.FormatInt(conversation.ID, 10),
		Unread:     conversation.Unread,
		Accounts:   accounts,
		LastStatus: lastStatus,
	}
}

func AccountFromModel(cfg config.Config, account models.Account) Account {
	return accountFromModel(cfg, account, true)
}

func accountFromModel(cfg config.Config, account models.Account, includeMoved bool) Account {
	stats := account.AccountStat
	lastStatusAt := dateString(stats.LastStatusAt)

	suspended := account.SuspendedAt.Valid
	limited := account.SilencedAt.Valid
	memorial := account.Memorial
	var noIndex *bool
	if account.Local() {
		v := boolSetting(userSettings(account.User), "noindex", false)
		noIndex = &v
	}
	var moved *Account
	if !suspended && includeMoved && account.MovedToAccount != nil && account.MovedToAccount.ID != 0 {
		item := accountFromModel(cfg, *account.MovedToAccount, false)
		moved = &item
	}
	emojis := customEmojis(cfg, account.CustomEmojis)
	fields := fieldsFromJSON(cfg, account)
	if suspended {
		emojis = []CustomEmoji{}
		fields = []Field{}
	}

	discoverable := boolPtr(account.Discoverable)
	if suspended {
		discoverable = boolPtrIf(true, false)
	}

	return Account{
		ID:              strconv.FormatInt(account.ID, 10),
		Username:        account.Username,
		Acct:            account.Acct(),
		DisplayName:     emptyIf(suspended, account.DisplayName),
		Locked:          !suspended && account.Locked,
		Bot:             !suspended && account.ActorType.Valid && (account.ActorType.String == "Application" || account.ActorType.String == "Service"),
		Discoverable:    discoverable,
		Indexable:       !suspended && account.Indexable,
		HideCollections: boolPtr(account.HideCollections),
		Group:           account.ActorType.Valid && account.ActorType.String == "Group",
		CreatedAt:       accountCreatedAt(account.CreatedAt),
		Note:            emptyIf(suspended, accountBioHTML(cfg, account)),
		URL:             accountURL(cfg, account),
		URI:             accountURI(cfg, account),
		Avatar:          accountAvatar(cfg, account, false),
		AvatarStatic:    accountAvatar(cfg, account, true),
		Header:          accountHeader(cfg, account, false),
		HeaderStatic:    accountHeader(cfg, account, true),
		FollowersCount:  stats.FollowersCount,
		FollowingCount:  stats.FollowingCount,
		StatusesCount:   stats.StatusesCount,
		LastStatusAt:    lastStatusAt,
		Moved:           moved,
		Emojis:          emojis,
		Fields:          fields,
		Roles:           AccountRolesFromModel(account),
		Local:           account.Local(),
		Suspended:       boolPtrIf(suspended, suspended),
		Limited:         boolPtrIf(limited, limited),
		Memorial:        boolPtrIf(memorial, memorial),
		NoIndex:         noIndex,
	}
}

func accountCreatedAt(createdAt time.Time) string {
	utc := createdAt.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
}

func restTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func statusStatCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func AccountRolesFromModel(account models.Account) []any {
	if !account.Local() || account.SuspendedAt.Valid || account.User.ID == 0 || !account.User.Role.Highlighted {
		return []any{}
	}
	return []any{AccountRole{
		ID:    strconv.FormatInt(account.User.Role.ID, 10),
		Name:  account.User.Role.Name,
		Color: account.User.Role.Color,
	}}
}

func SuggestionFromModel(cfg config.Config, account models.Account, source string) Suggestion {
	return SuggestionFromModelWithSources(cfg, account, []string{source})
}

func SuggestionFromModelWithSources(cfg config.Config, account models.Account, sources []string) Suggestion {
	sources = append([]string(nil), sources...)
	legacySource := "global"
	if len(sources) > 0 {
		legacySource = legacySuggestionSource(sources[0])
	}
	return Suggestion{
		Source:  legacySource,
		Sources: sources,
		Account: AccountFromModel(cfg, account),
	}
}

func legacySuggestionSource(source string) string {
	switch source {
	case "featured":
		return "staff"
	case "friends_of_friends", "similar_to_recently_followed":
		return "past_interactions"
	case "most_followed", "most_interactions":
		return "global"
	default:
		return source
	}
}

func CredentialAccountFromModel(cfg config.Config, account models.Account, user models.User, followRequestsCount int64) CredentialAccount {
	return CredentialAccountFromModelWithRole(cfg, account, user, followRequestsCount, nil, nil)
}

func CredentialAccountFromModelWithRole(cfg config.Config, account models.Account, user models.User, followRequestsCount int64, role *models.UserRole, everyone *models.UserRole) CredentialAccount {
	base := AccountFromModel(cfg, account)
	settings := userSettings(user)
	var rolePayload any
	if role != nil {
		rolePayload = RoleFromModel(*role, everyone)
	}
	return CredentialAccount{
		Account: base,
		Source: CredentialSource{
			Privacy:             UserDefaultPrivacy(settings, account),
			Sensitive:           boolSetting(settings, "default_sensitive", false),
			Language:            stringSettingPtr(settings, "default_language"),
			QuotePolicy:         stringSetting(settings, "default_quote_policy", "public"),
			Note:                account.Note,
			Fields:              sourceFieldsFromJSON(account.Fields),
			FollowRequestsCount: followRequestsCount,
			HideCollections:     boolPtr(account.HideCollections),
			Discoverable:        boolPtr(account.Discoverable),
			Indexable:           account.Indexable,
			AttributionDomains:  append([]string{}, account.AttributionDomains...),
		},
		Role: rolePayload,
	}
}

func PreferencesFromModel(cfg config.Config, user models.User, account models.Account) Preferences {
	settings := userSettings(user)
	return Preferences{
		PostingDefaultVisibility:  UserDefaultPrivacy(settings, account),
		PostingDefaultSensitive:   boolSetting(settings, "default_sensitive", false),
		PostingDefaultLanguage:    preferredPostingLanguage(settings, user, cfg),
		PostingDefaultQuotePolicy: stringSetting(settings, "default_quote_policy", "public"),
		ReadingExpandMedia:        stringSetting(settings, "web.display_media", "default"),
		ReadingExpandSpoilers:     boolSetting(settings, "web.expand_content_warnings", false),
		ReadingAutoplayGIFs:       boolSetting(settings, "web.auto_play", false),
	}
}

func UserDefaultPrivacy(settings map[string]any, account models.Account) string {
	if value := stringSetting(settings, "default_privacy", ""); value != "" {
		return value
	}
	if account.Locked {
		return "private"
	}
	return "public"
}

func repliesPolicyName(value int) string {
	switch value {
	case 1:
		return "followed"
	case 2:
		return "none"
	default:
		return "list"
	}
}

func MutedAccountFromModel(cfg config.Config, account models.Account, expiresAt sql.NullTime) MutedAccount {
	var expires *string
	if expiresAt.Valid && expiresAt.Time.After(time.Now().UTC()) {
		value := restTimestamp(expiresAt.Time)
		expires = &value
	}
	return MutedAccount{
		Account:       AccountFromModel(cfg, account),
		MuteExpiresAt: expires,
	}
}

func StatusFromModel(cfg config.Config, status models.Status, currentAccount *models.Account) Status {
	return statusFromModel(cfg, status, currentAccount, false)
}

func statusFromModel(cfg config.Config, status models.Status, currentAccount *models.Account, shallow bool) Status {
	statusURL, statusURLNull := statusURLValue(cfg, status)
	reblogsCount, favouritesCount := statusInteractionCounts(status)
	policyStatus := status
	if status.Reblog != nil && status.Reblog.ID != 0 {
		policyStatus = *status.Reblog
	}
	currentQuotePolicy := policyStatus.QuotePolicyCurrentUser
	if currentQuotePolicy == "" {
		currentQuotePolicy = "denied"
	}
	item := Status{
		ID:                 strconv.FormatInt(status.ID, 10),
		CreatedAt:          restTimestamp(status.CreatedAt),
		InReplyToID:        idPtr(status.InReplyToID),
		InReplyToAccountID: idPtr(status.InReplyToAccountID),
		Sensitive:          statusSensitiveFromModel(status, currentAccount),
		SpoilerText:        status.SpoilerText,
		Visibility:         visibilityName(status.Visibility),
		Language:           stringPtr(status.Language),
		URI:                statusURI(cfg, status),
		URL:                statusURL,
		URLNull:            statusURLNull,
		RepliesCount:       statusStatCount(status.StatusStat.RepliesCount),
		ReblogsCount:       reblogsCount,
		FavouritesCount:    favouritesCount,
		QuotesCount:        statusStatCount(status.StatusStat.QuotesCount),
		EditedAt:           timePtr(status.EditedAt),
		Content:            statusContentHTML(cfg, status),
		Account:            AccountFromModel(cfg, status.Account),
		Application:        statusApplicationFromModel(status, currentAccount),
		ApplicationPresent: showStatusApplication(status, currentAccount),
		MediaAttachments:   mediaAttachments(cfg, orderedStatusMediaAttachments(status)),
		Mentions:           mentions(cfg, status.Mentions),
		Tags:               tags(cfg, status.Tags),
		Emojis:             customEmojis(cfg, status.CustomEmojis),
		Card:               previewCardFromStatus(cfg, status),
		Poll:               PollFromModel(cfg, status.Poll, currentAccount),
		QuoteApproval: StatusQuoteApproval{
			Automatic:   quotePolicyKeyNames(policyStatus.QuoteApprovalPolicy >> 16),
			Manual:      quotePolicyKeyNames(policyStatus.QuoteApprovalPolicy & 0xffff),
			CurrentUser: currentQuotePolicy,
		},
	}
	item.Quote = quoteFromModel(cfg, status.Quote, currentAccount, shallow)

	if status.Reblog != nil && status.Reblog.ID != 0 {
		reblog := statusFromModel(cfg, *status.Reblog, currentAccount, false)
		item.Reblog = &reblog
	}

	if currentAccount != nil {
		favourited := status.FavouritedByCurrent
		reblogged := status.RebloggedByCurrent
		muted := status.MutedByCurrent
		bookmarked := status.BookmarkedByCurrent
		item.Favourited = &favourited
		item.Reblogged = &reblogged
		item.Muted = &muted
		item.Bookmarked = &bookmarked
		item.Filtered = []any{}
		item.FilteredPresent = true
		if currentAccount.ID == status.AccountID && status.ReblogOfID.Valid == false && status.Visibility <= 2 {
			pinned := status.PinnedByCurrent
			item.Pinned = &pinned
		}
	}

	return item
}

func statusInteractionCounts(status models.Status) (int64, int64) {
	reblogs := status.StatusStat.ReblogsCount
	favourites := status.StatusStat.FavouritesCount
	if !statusLocal(status) {
		if status.StatusStat.UntrustedReblogsCount.Valid {
			reblogs = status.StatusStat.UntrustedReblogsCount.Int64
		}
		if status.StatusStat.UntrustedFavouritesCount.Valid {
			favourites = status.StatusStat.UntrustedFavouritesCount.Int64
		}
	}
	return statusStatCount(reblogs), statusStatCount(favourites)
}

func quoteFromModel(cfg config.Config, quote *models.Quote, currentAccount *models.Account, shallow bool) any {
	return quoteFromModelWithSource(cfg, quote, currentAccount, shallow, false)
}

func quoteFromModelWithSource(cfg config.Config, quote *models.Quote, currentAccount *models.Account, shallow bool, sourceRequested bool) any {
	if quote == nil || (quote.State != models.QuoteStateAccepted && quote.Legacy) {
		return nil
	}
	state := quoteStateName(quote)
	accepted := quote.State == models.QuoteStateAccepted
	targetAvailable := quote.QuotedStatus != nil && quote.QuotedStatus.ID != 0 && !quote.QuotedStatus.DeletedAt.Valid && !quote.QuotedStatus.ReblogOfID.Valid
	available := targetAvailable && (accepted || sourceRequested)
	visible := targetAvailable && quoteStatusVisibleWithoutDatabase(*quote.QuotedStatus, currentAccount)
	if quote.QuotedStatusVisibilityChecked {
		visible = quote.QuotedStatusVisible
	}
	if targetAvailable && !visible {
		filterState := quote.QuotedStatusFilterState
		if filterState == "" {
			filterState = "unauthorized"
		}
		if accepted {
			state = filterState
		}
		// Mastodon exposes the nested status/ID for viewer-owned block, domain
		// block, and mute states so clients can offer an explicit reveal action.
		// An author-side visibility denial remains opaque.
		available = available && (filterState == "blocked_account" || filterState == "blocked_domain" || filterState == "muted_account")
	}
	if shallow {
		var quotedStatusID *string
		if available {
			value := strconv.FormatInt(quote.QuotedStatus.ID, 10)
			quotedStatusID = &value
		}
		return ShallowQuote{State: state, QuotedStatusID: quotedStatusID}
	}
	var quotedStatus *Status
	if available {
		value := statusFromModel(cfg, *quote.QuotedStatus, currentAccount, true)
		quotedStatus = &value
	}
	return Quote{State: state, QuotedStatus: quotedStatus}
}

func quotePolicyKeyNames(policy int) []string {
	out := make([]string, 0, 4)
	for _, item := range []struct {
		name string
		flag int
	}{
		{"unsupported_policy", 1 << 0},
		{"public", 1 << 1},
		{"followers", 1 << 2},
		{"following", 1 << 3},
	} {
		if policy&item.flag != 0 {
			out = append(out, item.name)
		}
	}
	return out
}

func quoteStatusVisibleWithoutDatabase(status models.Status, currentAccount *models.Account) bool {
	if status.Visibility <= 1 {
		return true
	}
	if currentAccount == nil || currentAccount.ID == 0 {
		return false
	}
	if status.AccountID == currentAccount.ID {
		return true
	}
	for _, mention := range status.Mentions {
		if mention.AccountID.Valid && mention.AccountID.Int64 == currentAccount.ID {
			return true
		}
	}
	// Followers-only visibility requires a database relationship check. The
	// serializer fails closed if the caller did not hydrate an authorized copy.
	return false
}

func quoteStateName(quote *models.Quote) string {
	if quote == nil {
		return "pending"
	}
	if quote.State == models.QuoteStateAccepted && (quote.QuotedStatus == nil || quote.QuotedStatus.ID == 0 || quote.QuotedStatus.DeletedAt.Valid) {
		return "deleted"
	}
	switch quote.State {
	case models.QuoteStateAccepted:
		return "accepted"
	case models.QuoteStateRejected:
		return "rejected"
	case models.QuoteStateRevoked:
		return "revoked"
	case models.QuoteStateDeleted:
		return "deleted"
	default:
		return "pending"
	}
}

func StatusFromModelWithSource(cfg config.Config, status models.Status, currentAccount *models.Account) Status {
	item := StatusFromModel(cfg, status, currentAccount)
	item.Text = &status.Text
	item.Quote = quoteFromModelWithSource(cfg, status.Quote, currentAccount, false, true)
	return item
}

func statusApplicationFromModel(status models.Status, currentAccount *models.Account) *StatusApplication {
	if status.Application == nil || status.Application.ID == 0 || !showStatusApplication(status, currentAccount) {
		return nil
	}
	var website *string
	if strings.TrimSpace(string(status.Application.Website)) != "" {
		value := string(status.Application.Website)
		website = &value
	}
	return &StatusApplication{Name: status.Application.Name, Website: website}
}

func showStatusApplication(status models.Status, currentAccount *models.Account) bool {
	if currentAccount != nil && currentAccount.ID == status.AccountID {
		return true
	}
	if status.Account.User.ID == 0 {
		return false
	}
	return boolSetting(userSettings(status.Account.User), "show_application", true)
}

func statusSensitiveFromModel(status models.Status, currentAccount *models.Account) bool {
	if currentAccount != nil && currentAccount.ID == status.AccountID {
		return status.Sensitive
	}
	return status.Sensitive || status.Account.SensitizedAt.Valid
}

func StatusSourceFromModel(status models.Status) StatusSource {
	return StatusSource{
		ID:          strconv.FormatInt(status.ID, 10),
		Text:        status.Text,
		SpoilerText: status.SpoilerText,
	}
}

func StatusEditFromModel(cfg config.Config, edit models.StatusEdit) StatusEdit {
	var sensitive *bool
	if edit.Sensitive.Valid {
		value := edit.Sensitive.Bool
		sensitive = &value
	}
	var poll *StatusEditPoll
	if len(edit.PollOptions) > 0 {
		options := make([]StatusEditPollOption, 0, len(edit.PollOptions))
		for _, title := range edit.PollOptions {
			options = append(options, StatusEditPollOption{Title: title})
		}
		poll = &StatusEditPoll{Options: options}
	}
	var account *Account
	if edit.AccountID.Valid && edit.Account.ID != 0 {
		value := AccountFromModel(cfg, edit.Account)
		account = &value
	}
	media := edit.OrderedMediaAttachments
	if edit.Status.DeletedAt.Valid {
		media = markMediaAttachmentsDiscarded(media)
	}
	item := StatusEdit{
		Content:          statusEditContentHTML(cfg, edit),
		SpoilerText:      edit.SpoilerText,
		Sensitive:        sensitive,
		CreatedAt:        restTimestamp(edit.CreatedAt),
		Account:          account,
		MediaAttachments: mediaAttachments(cfg, media),
		Emojis:           customEmojis(cfg, edit.CustomEmojis),
		Poll:             poll,
	}
	if edit.QuoteID.Valid {
		if edit.Status.Quote != nil && edit.Status.Quote.ID == edit.QuoteID.Int64 {
			if value, ok := quoteFromModel(cfg, edit.Status.Quote, nil, false).(Quote); ok {
				item.Quote = &value
			}
		} else {
			item.Quote = &Quote{State: "pending"}
		}
	}
	return item
}

func statusEditContentHTML(cfg config.Config, edit models.StatusEdit) string {
	status := edit.Status
	status.Text = edit.Text
	status.CustomEmojis = edit.CustomEmojis
	if edit.Account.ID != 0 {
		status.Account = edit.Account
		status.AccountID = edit.Account.ID
		status.Mentions = []models.Mention{{AccountID: models.MentionAccountID(edit.Account.ID), Account: edit.Account}}
	}
	return statusContentHTML(cfg, status)
}

func PollFromModel(cfg config.Config, poll *models.Poll, currentAccount *models.Account) *Poll {
	if poll == nil || poll.ID == 0 {
		return nil
	}
	expired := poll.ExpiresAt.Valid && poll.ExpiresAt.Time.Before(time.Now().UTC())
	showTotals := expired || !poll.HideTotals
	options := make([]PollOption, 0, len(poll.Options))
	for index, title := range poll.Options {
		var votes *int64
		if showTotals {
			count := int64(0)
			if index < len(poll.CachedTallies) {
				count = poll.CachedTallies[index]
			}
			votes = &count
		}
		options = append(options, PollOption{Title: title, VotesCount: votes})
	}
	var expiresAt *string
	if poll.ExpiresAt.Valid {
		value := restTimestamp(poll.ExpiresAt.Time)
		expiresAt = &value
	}
	var votersCount *int64
	if poll.VotersCount.Valid {
		votersCount = &poll.VotersCount.Int64
	}
	out := &Poll{
		ID:          strconv.FormatInt(poll.ID, 10),
		ExpiresAt:   expiresAt,
		Expired:     expired,
		Multiple:    poll.Multiple,
		VotesCount:  poll.VotesCount,
		VotersCount: votersCount,
		Options:     options,
		Emojis:      customEmojis(cfg, poll.CustomEmojis),
	}
	if currentAccount != nil {
		voted := poll.AccountID.Valid && currentAccount.ID == poll.AccountID.Int64
		ownVotes := []int{}
		for _, vote := range poll.Votes {
			if vote.AccountID.Valid && vote.AccountID.Int64 == currentAccount.ID {
				voted = true
				ownVotes = append(ownVotes, vote.Choice)
			}
		}
		out.Voted = &voted
		out.OwnVotes = &ownVotes
	}
	return out
}

type InstanceRegistrationOptions struct {
	Mode           string
	ClosedMessage  string
	SignUpURL      string
	SignUpURLSet   bool
	ReasonRequired bool
	MinimumAge     *int
}

type InstanceMetadata struct {
	Title             string
	TitleSet          bool
	ShortDescription  string
	Description       string
	ContactEmail      string
	ContactAccount    *models.Account
	Thumbnail         *models.SiteUpload
	AppIcon           *models.SiteUpload
	AppIconURLs       map[string]string
	PreviewImageURL   string
	Rules             []models.Rule
	StatusPageURL     string
	TermsOfServiceURL string
	TimelinesAccess   map[string]any
}

func InstanceFromConfig(cfg config.Config, stats map[string]string) Instance {
	return InstanceFromConfigWithRegistrations(cfg, stats, InstanceRegistrationOptions{Mode: "none"})
}

func InstanceFromConfigWithRegistrations(cfg config.Config, stats map[string]string, registrations InstanceRegistrationOptions) Instance {
	return InstanceFromConfigWithRegistrationsAndUsage(cfg, stats, registrations, nil)
}

func InstanceFromConfigWithRegistrationsAndUsage(cfg config.Config, stats map[string]string, registrations InstanceRegistrationOptions, activeMonth *int64) Instance {
	return InstanceFromConfigWithOptions(cfg, stats, registrations, activeMonth, InstanceMetadata{})
}

func InstanceFromConfigWithOptions(cfg config.Config, stats map[string]string, registrations InstanceRegistrationOptions, activeMonth *int64, metadata InstanceMetadata) Instance {
	enabled := registrations.Mode != "none" && !cfg.SingleUserMode
	approvalRequired := registrations.Mode == "approved"
	var message any
	if !enabled && strings.TrimSpace(registrations.ClosedMessage) != "" {
		message = simpleMarkdownHTML(registrations.ClosedMessage)
	}
	var signUpURL any
	if registrations.SignUpURLSet || strings.TrimSpace(registrations.SignUpURL) != "" {
		signUpURL = registrations.SignUpURL
	}
	var activeMonthValue any
	if activeMonth != nil {
		activeMonthValue = *activeMonth
	}
	var contactAccount any
	if metadata.ContactAccount != nil {
		contactAccount = AccountFromModel(cfg, *metadata.ContactAccount)
	}
	title := metadata.Title
	if !metadata.TitleSet && title == "" {
		title = cfg.Title
	}

	return Instance{
		Domain:        cfg.LocalDomain,
		Title:         title,
		Version:       instanceVersion(cfg),
		ActualVersion: cfg.Version,
		SourceURL:     cfg.SourceURL,
		Description:   metadata.ShortDescription,
		Usage: map[string]any{
			"users": map[string]any{"active_month": activeMonthValue},
		},
		Thumbnail: InstanceThumbnailFromSiteUpload(cfg, metadata.Thumbnail, metadata.PreviewImageURL),
		Icon:      instanceIcons(cfg, metadata.AppIcon, metadata.AppIconURLs),
		Languages: []string{cfg.Locale()},
		Configuration: map[string]any{
			"urls": map[string]any{
				"streaming":        cfg.StreamingBaseURL(),
				"status":           optionalStringAny(metadata.StatusPageURL),
				"about":            cfg.BaseURL() + "/about",
				"privacy_policy":   cfg.BaseURL() + "/privacy-policy",
				"terms_of_service": optionalStringAny(metadata.TermsOfServiceURL),
			},
			"accounts": map[string]any{
				"max_display_name_length":       40,
				"max_note_length":               500,
				"max_avatar_description_length": 150,
				"max_header_description_length": 150,
				"max_featured_tags":             10,
				"max_pinned_statuses":           5,
				"max_profile_fields":            4,
			},
			"vapid": map[string]any{
				"public_key": optionalStringAny(cfg.VapidPublicKey),
			},
			"statuses": map[string]any{
				"max_characters":              statusMaxChars(cfg),
				"max_media_attachments":       mediaAttachmentLimit(cfg),
				"characters_reserved_per_url": 23,
			},
			"media_attachments": map[string]any{
				"supported_mime_types":   append([]string{}, mediaAttachmentMimeTypes...),
				"description_limit":      1_500,
				"image_size_limit":       imageSizeLimit(cfg),
				"image_matrix_limit":     matrixLimit(cfg),
				"video_size_limit":       videoSizeLimit(cfg),
				"video_frame_rate_limit": 120,
				"video_matrix_limit":     8_294_400,
			},
			"polls": map[string]any{
				"max_options":               4,
				"max_characters_per_option": 50,
				"min_expiration":            300,
				"max_expiration":            2_629_746,
			},
			"translation": map[string]any{
				"enabled": translationEnabled(cfg),
			},
			"timelines_access":   metadata.TimelinesAccess,
			"limited_federation": cfg.LimitedFederationMode,
		},
		Registrations: map[string]any{
			"enabled":           enabled,
			"approval_required": approvalRequired,
			"reason_required":   approvalRequired && registrations.ReasonRequired,
			"message":           message,
			"min_age":           optionalIntAny(registrations.MinimumAge),
			"url":               signUpURL,
		},
		Contact: map[string]any{
			"email":   metadata.ContactEmail,
			"account": contactAccount,
		},
		Rules:       InstanceRulesFromModels(metadata.Rules),
		APIVersions: map[string]int{"mastodon": 7},
		Stats:       stats,
		URI:         cfg.LocalDomain,
	}
}

func instanceIcons(cfg config.Config, appIcon *models.SiteUpload, fallbackURLs map[string]string) []map[string]string {
	sizes := []int{36, 48, 72, 96, 144, 192, 256, 384, 512}
	out := make([]map[string]string, 0, len(sizes))
	for _, size := range sizes {
		dimensions := strconv.Itoa(size) + "x" + strconv.Itoa(size)
		src := cfg.BaseURL() + "/android-chrome-" + dimensions + ".png"
		if fallback := strings.TrimSpace(fallbackURLs[dimensions]); fallback != "" {
			src = fallback
		}
		if appIcon != nil {
			if custom := SiteUploadFileURL(cfg, *appIcon, strconv.Itoa(size)); custom != "" {
				src = custom
			}
		}
		out = append(out, map[string]string{
			"src":  src,
			"size": dimensions,
		})
	}
	return out
}

func InstanceThumbnailFromSiteUpload(cfg config.Config, upload *models.SiteUpload, fallbackURL string) map[string]any {
	if upload == nil || !upload.FileFileName.Valid || upload.FileFileName.String == "" {
		return map[string]any{"url": fallbackPreviewImageURL(cfg, fallbackURL)}
	}
	oneX := siteUploadAssetURL(cfg, upload.ID, "@1x", siteUploadStyleFilename(upload.Var, "@1x", upload.FileFileName.String))
	twoX := siteUploadAssetURL(cfg, upload.ID, "@2x", siteUploadStyleFilename(upload.Var, "@2x", upload.FileFileName.String))
	out := map[string]any{
		"url": oneX,
		"versions": map[string]string{
			"@1x": oneX,
			"@2x": twoX,
		},
	}
	if upload.Blurhash.Valid && upload.Blurhash.String != "" {
		out["blurhash"] = upload.Blurhash.String
	}
	return out
}

func InstanceV1ThumbnailFromSiteUpload(cfg config.Config, upload *models.SiteUpload, fallbackURL string) string {
	if upload == nil || !upload.FileFileName.Valid || upload.FileFileName.String == "" {
		return fallbackPreviewImageURL(cfg, fallbackURL)
	}
	return siteUploadAssetURL(cfg, upload.ID, "@1x", siteUploadStyleFilename(upload.Var, "@1x", upload.FileFileName.String))
}

func SiteUploadFileURL(cfg config.Config, upload models.SiteUpload, style string) string {
	if !upload.FileFileName.Valid || upload.FileFileName.String == "" {
		return ""
	}
	if strings.TrimSpace(style) == "" {
		style = "original"
	}
	return siteUploadAssetURL(cfg, upload.ID, style, siteUploadStyleFilename(upload.Var, style, upload.FileFileName.String))
}

func InstanceRulesFromModels(rules []models.Rule) []any {
	out := make([]any, 0, len(rules))
	for _, rule := range rules {
		translations := make(map[string]InstanceRuleTranslation, len(rule.Translations))
		for _, translation := range rule.Translations {
			translations[translation.Language] = InstanceRuleTranslation{Text: translation.Text, Hint: translation.Hint}
		}
		out = append(out, InstanceRule{
			ID:           strconv.FormatInt(rule.ID, 10),
			Text:         rule.Text,
			Hint:         rule.Hint,
			Translations: translations,
		})
	}
	return out
}

func siteUploadAssetURL(cfg config.Config, id int64, style string, filename string) string {
	return cfg.SystemAssetURL("site_uploads/files/" + paperclipIDPartition(id) + "/" + style + "/" + url.PathEscape(filename))
}

func siteUploadStyleFilename(name string, style string, filename string) string {
	if (name == "thumbnail" || name == "favicon" || name == "app_icon") && style != "original" {
		extIndex := strings.LastIndex(filename, ".")
		if extIndex > 0 {
			return filename[:extIndex] + ".png"
		}
		return filename + ".png"
	}
	return filename
}

func fallbackPreviewImageURL(cfg config.Config, fallbackURL string) string {
	if strings.TrimSpace(fallbackURL) != "" {
		return fallbackURL
	}
	return cfg.BaseURL() + "/packs/media/images/preview.png"
}

func instanceVersion(cfg config.Config) string {
	mastodonVersion := strings.TrimSpace(cfg.MastodonVersion)
	if mastodonVersion == "" {
		mastodonVersion = config.DefaultMastodonVersion
	}
	return mastodonVersion
}

func optionalStringAny(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func optionalIntAny(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func translationEnabled(cfg config.Config) bool {
	return strings.TrimSpace(cfg.DeepLAPIKey) != "" || strings.TrimSpace(cfg.LibreTranslateEndpoint) != ""
}

func positiveOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func mediaAttachmentLimit(cfg config.Config) int {
	if cfg.MaxMediaSet || cfg.MaxMedia > 0 {
		return cfg.MaxMedia
	}
	return 4
}

func statusMaxChars(cfg config.Config) int {
	if cfg.StatusMaxCharsSet || cfg.StatusMaxChars > 0 {
		return cfg.StatusMaxChars
	}
	return 5000
}

func imageSizeLimit(cfg config.Config) int {
	if cfg.ImageSizeLimitSet || cfg.ImageSizeLimit > 0 {
		return cfg.ImageSizeLimit
	}
	return 40 * 1024 * 1024
}

func videoSizeLimit(cfg config.Config) int {
	if cfg.VideoSizeLimitSet || cfg.VideoSizeLimit > 0 {
		return cfg.VideoSizeLimit
	}
	return 90 * 1024 * 1024
}

func matrixLimit(cfg config.Config) int {
	if cfg.MatrixLimitSet || cfg.MatrixLimit > 0 {
		return cfg.MatrixLimit
	}
	return 16_777_216
}

func MediaAttachmentFromModel(cfg config.Config, attachment models.MediaAttachment) MediaAttachment {
	items := mediaAttachments(cfg, []models.MediaAttachment{attachment})
	if len(items) == 0 {
		return MediaAttachment{
			ID:         strconv.FormatInt(attachment.ID, 10),
			Type:       "unknown",
			URL:        "",
			PreviewURL: "",
			Meta:       map[string]any{},
		}
	}
	return items[0]
}

func PreviewCardFromModel(cfg config.Config, card models.PreviewCard) PreviewCard {
	authors := []PreviewCardAuthor{}
	if card.AuthorName != "" || card.AuthorURL != "" || card.AuthorAccountID.Valid {
		var authorAccount *Account
		if card.AuthorAccount != nil && card.AuthorAccount.ID != 0 {
			serialized := AccountFromModel(cfg, *card.AuthorAccount)
			authorAccount = &serialized
		}
		authors = append(authors, PreviewCardAuthor{Name: card.AuthorName, URL: card.AuthorURL, Account: authorAccount})
	}
	return PreviewCard{
		URL:              card.URL,
		Title:            card.Title,
		Description:      card.Description,
		Language:         stringPtr(card.Language),
		Type:             previewCardType(card.Type),
		AuthorName:       card.AuthorName,
		AuthorURL:        card.AuthorURL,
		ProviderName:     card.ProviderName,
		ProviderURL:      card.ProviderURL,
		HTML:             sanitizePreviewCardOEmbedHTML(card.HTML),
		Width:            card.Width,
		Height:           card.Height,
		Image:            previewCardImageURL(cfg, card),
		ImageDescription: card.ImageDescription,
		EmbedURL:         card.EmbedURL,
		Blurhash:         stringPtr(card.Blurhash),
		PublishedAt:      timePtr(card.PublishedAt),
		Authors:          authors,
	}
}

func sanitizePreviewCardOEmbedHTML(value string) string {
	value = previewCardOEmbedScriptBlockPattern.ReplaceAllString(value, "")
	var out strings.Builder
	last := 0
	for _, match := range previewCardOEmbedHTMLTagPattern.FindAllStringSubmatchIndex(value, -1) {
		if match[0] > last {
			out.WriteString(html.EscapeString(value[last:match[0]]))
		}
		closing := value[match[2]:match[3]] != ""
		name := strings.ToLower(value[match[4]:match[5]])
		attrText := ""
		if match[6] >= 0 {
			attrText = value[match[6]:match[7]]
		}
		if previewCardOEmbedAllowedElement(name) {
			if closing {
				if name != "source" {
					out.WriteString("</" + name + ">")
				}
			} else {
				out.WriteString("<" + name + sanitizePreviewCardOEmbedAttrs(name, attrText) + ">")
			}
		}
		last = match[1]
	}
	if last < len(value) {
		out.WriteString(html.EscapeString(value[last:]))
	}
	return out.String()
}

func previewCardOEmbedAllowedElement(name string) bool {
	switch name {
	case "audio", "iframe", "source", "video":
		return true
	default:
		return false
	}
}

func sanitizePreviewCardOEmbedAttrs(name string, attrText string) string {
	attrs := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, match := range previewCardOEmbedAttrPattern.FindAllStringSubmatch(attrText, -1) {
		if len(match) < 5 {
			continue
		}
		key := strings.ToLower(match[1])
		if !previewCardOEmbedAllowedAttr(name, key) {
			continue
		}
		value := strings.TrimSpace(firstNonEmptyString(match[2], match[3], match[4]))
		if (name == "iframe" || name == "source") && key == "src" && !previewCardOEmbedHTTPURLAllowed(value) {
			continue
		}
		attrs = append(attrs, key+`="`+html.EscapeString(value)+`"`)
		seen[key] = struct{}{}
	}
	for _, token := range strings.Fields(attrText) {
		key := strings.ToLower(strings.Trim(token, "/"))
		if key == "" || strings.Contains(key, "=") || !previewCardOEmbedAllowedAttr(name, key) || !previewCardOEmbedBooleanAttr(key) {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		attrs = append(attrs, key+`=""`)
		seen[key] = struct{}{}
	}
	if name == "iframe" {
		attrs = append(attrs, `sandbox="allow-scripts allow-same-origin allow-popups allow-popups-to-escape-sandbox allow-forms"`)
	}
	if len(attrs) == 0 {
		return ""
	}
	return " " + strings.Join(attrs, " ")
}

func previewCardOEmbedAllowedAttr(name string, key string) bool {
	switch name {
	case "audio":
		return key == "controls"
	case "iframe":
		switch key {
		case "allowfullscreen", "frameborder", "height", "scrolling", "src", "width":
			return true
		}
	case "source":
		return key == "src" || key == "type"
	case "video":
		switch key {
		case "controls", "height", "loop", "width":
			return true
		}
	}
	return false
}

func previewCardOEmbedBooleanAttr(key string) bool {
	switch key {
	case "allowfullscreen", "controls", "loop":
		return true
	default:
		return false
	}
}

func previewCardOEmbedHTTPURLAllowed(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func PreviewCardTrendLinkFromModel(cfg config.Config, card models.PreviewCard, uses int64, accounts int64, now time.Time) PreviewCardTrendLink {
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC).Unix()
	return PreviewCardTrendLinkFromModelWithHistory(cfg, card, []any{
		map[string]string{
			"day":      strconv.FormatInt(day, 10),
			"uses":     strconv.FormatInt(uses, 10),
			"accounts": strconv.FormatInt(accounts, 10),
		},
	})
}

func PreviewCardTrendLinkFromModelWithHistory(cfg config.Config, card models.PreviewCard, history []any) PreviewCardTrendLink {
	if history == nil {
		history = []any{}
	}
	return PreviewCardTrendLink{
		PreviewCard: PreviewCardFromModel(cfg, card),
		History:     history,
	}
}

func AdminTrendLinkFromModel(cfg config.Config, card models.PreviewCard, uses int64, accounts int64, now time.Time, requiresReview bool) AdminTrendLink {
	return AdminTrendLinkFromModelWithHistory(cfg, card, PreviewCardTrendLinkFromModel(cfg, card, uses, accounts, now).History, requiresReview)
}

func AdminTrendLinkFromModelWithHistory(cfg config.Config, card models.PreviewCard, history []any, requiresReview bool) AdminTrendLink {
	return AdminTrendLink{
		PreviewCardTrendLink: PreviewCardTrendLinkFromModelWithHistory(cfg, card, history),
		ID:                   strconv.FormatInt(card.ID, 10),
		RequiresReview:       requiresReview,
	}
}

func AdminTrendStatusFromModel(cfg config.Config, status models.Status, currentAccount *models.Account) AdminTrendStatus {
	return AdminTrendStatus{
		Status:         StatusFromModel(cfg, status, currentAccount),
		RequiresReview: !status.Trendable.Valid && !status.Account.ReviewedAt.Valid,
	}
}

func AdminPreviewCardProviderFromModel(provider models.PreviewCardProvider) AdminPreviewCardProvider {
	return AdminPreviewCardProvider{
		ID:                strconv.FormatInt(provider.ID, 10),
		Domain:            provider.Domain,
		Trendable:         boolPtr(provider.Trendable),
		ReviewedAt:        timePtr(provider.ReviewedAt),
		RequestedReviewAt: timePtr(provider.RequestedReviewAt),
		RequiresReview:    !provider.ReviewedAt.Valid,
	}
}

func ScheduledStatusFromModel(cfg config.Config, status models.ScheduledStatus) ScheduledStatus {
	params := map[string]any{}
	if len(status.Params) > 0 {
		_ = json.Unmarshal(status.Params, &params)
	}
	// Rails stores these values in scheduled_statuses.params using the model
	// representation (numeric IDs and a quote-policy bitmask), while the 4.5
	// REST entity exposes client-facing strings. Paon-created rows already use
	// strings, so accept both forms to keep the shared PostgreSQL schema
	// drop-in compatible in either direction.
	params["quoted_status_id"] = scheduledStatusQuotedStatusID(params["quoted_status_id"])
	params["quote_approval_policy"] = scheduledStatusQuoteApprovalPolicy(params["quote_approval_policy"])
	var scheduledAt *string
	if status.ScheduledAt.Valid {
		value := restTimestamp(status.ScheduledAt.Time)
		scheduledAt = &value
	}
	return ScheduledStatus{
		ID:               strconv.FormatInt(status.ID, 10),
		ScheduledAt:      scheduledAt,
		Params:           params,
		MediaAttachments: mediaAttachments(cfg, mediaAttachmentsSortedByID(status.MediaAttachments)),
	}
}

func scheduledStatusQuotedStatusID(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return typed
	case float64:
		integer := int64(typed)
		if float64(integer) == typed {
			return strconv.FormatInt(integer, 10)
		}
	}
	return nil
}

func scheduledStatusQuoteApprovalPolicy(value any) string {
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		switch text {
		case "public", "followers", "nobody", "unsupported_policy", "following":
			return text
		}
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return scheduledStatusQuoteApprovalPolicyFromBits(parsed)
		}
		return "nobody"
	}
	if number, ok := value.(float64); ok {
		integer := int64(number)
		if float64(integer) == number {
			return scheduledStatusQuoteApprovalPolicyFromBits(integer)
		}
	}
	return "nobody"
}

func scheduledStatusQuoteApprovalPolicyFromBits(value int64) string {
	automatic := value >> 16
	for _, item := range []struct {
		name string
		flag int64
	}{
		{"unsupported_policy", 1 << 0},
		{"public", 1 << 1},
		{"followers", 1 << 2},
		{"following", 1 << 3},
	} {
		if automatic&item.flag != 0 {
			return item.name
		}
	}
	return "nobody"
}

func NotificationFromModel(cfg config.Config, notification models.Notification, currentAccount *models.Account) Notification {
	groupKey := notification.GroupKey.String
	if groupKey == "" {
		groupKey = "ungrouped-" + strconv.FormatInt(notification.ID, 10)
	}
	item := Notification{
		ID:        strconv.FormatInt(notification.ID, 10),
		Type:      notification.ResolvedType(),
		CreatedAt: restTimestamp(notification.CreatedAt),
		GroupKey:  groupKey,
		Account:   AccountFromModel(cfg, notification.FromAccount),
	}
	if notification.Filtered {
		filtered := true
		item.Filtered = &filtered
	}
	if notificationStatusType(notification.ResolvedType()) && notification.TargetStatus != nil && notification.TargetStatus.ID != 0 {
		status := StatusFromModel(cfg, *notification.TargetStatus, currentAccount)
		item.Status = &status
	}
	if notification.ResolvedType() == "admin.report" && notification.Report != nil && notification.Report.ID != 0 {
		item.Report = ReportFromModel(cfg, *notification.Report)
	}
	if notification.ResolvedType() == "severed_relationships" && notification.SeveranceEvent != nil {
		event := AccountRelationshipSeveranceEventFromModel(*notification.SeveranceEvent)
		item.Event = &event
	}
	if notification.ResolvedType() == "moderation_warning" && notification.AccountWarning != nil {
		warning := AccountWarningFromModel(cfg, *notification.AccountWarning)
		item.ModerationWarning = &warning
	}
	return item
}

func AccountRelationshipSeveranceEventFromModel(event models.AccountRelationshipSeveranceEvent) AccountRelationshipSeveranceEvent {
	return AccountRelationshipSeveranceEvent{
		ID: strconv.FormatInt(event.ID, 10), Type: relationshipSeveranceEventType(event.RelationshipSeveranceEvent.Type), Purged: event.RelationshipSeveranceEvent.Purged,
		TargetName: event.RelationshipSeveranceEvent.TargetName, FollowersCount: event.FollowersCount, FollowingCount: event.FollowingCount,
		CreatedAt: restTimestamp(event.CreatedAt),
	}
}

func relationshipSeveranceEventType(value int) string {
	switch value {
	case 0:
		return "domain_block"
	case 1:
		return "user_domain_block"
	case 2:
		return "account_suspension"
	default:
		return "domain_block"
	}
}

func AccountWarningFromModel(cfg config.Config, warning models.AccountWarning) AccountWarning {
	statusIDs := make([]string, 0, len(warning.StatusIDs))
	for _, id := range warning.StatusIDs {
		statusIDs = append(statusIDs, id)
	}
	return AccountWarning{
		ID: strconv.FormatInt(warning.ID, 10), Action: accountWarningAction(warning.Action), Text: warning.Text, StatusIDs: statusIDs,
		CreatedAt: restTimestamp(warning.CreatedAt), TargetAccount: AccountFromModel(cfg, warning.TargetAccount), Appeal: nil,
	}
}

func accountWarningAction(value int) string {
	switch value {
	case 1000:
		return "disable"
	case 1250:
		return "mark_statuses_as_sensitive"
	case 1500:
		return "delete_statuses"
	case 2000:
		return "sensitive"
	case 3000:
		return "silence"
	case 4000:
		return "suspend"
	default:
		return "none"
	}
}

func notificationStatusType(kind string) bool {
	switch kind {
	case "favourite", "reblog", "status", "mention", "poll", "quote", "update", "quoted_update":
		return true
	default:
		return false
	}
}

func ReportFromModel(cfg config.Config, report models.Report) Report {
	return Report{
		ID:            strconv.FormatInt(report.ID, 10),
		ActionTaken:   report.ActionTakenAt.Valid,
		ActionTakenAt: timePtr(report.ActionTakenAt),
		Category:      reportCategoryName(report.Category),
		Comment:       report.Comment,
		Forwarded:     boolPtr(report.Forwarded),
		CreatedAt:     restTimestamp(report.CreatedAt),
		StatusIDs:     int64Strings(report.StatusIDs),
		RuleIDs:       int64Strings(report.RuleIDs),
		TargetAccount: AccountFromModel(cfg, report.TargetAccount),
	}
}

func AdminAccountFromModel(cfg config.Config, account models.Account) AdminAccount {
	return AdminAccountFromModelWithIPs(cfg, account, nil)
}

func AdminAccountFromModelWithIPs(cfg config.Config, account models.Account, ips []AdminAccountIP) AdminAccount {
	return AdminAccountFromModelWithOptions(cfg, account, AdminAccountOptions{IPs: ips})
}

func AdminAccountFromModelWithIPsAndRole(cfg config.Config, account models.Account, ips []AdminAccountIP, role *models.UserRole, everyone *models.UserRole) AdminAccount {
	return AdminAccountFromModelWithOptions(cfg, account, AdminAccountOptions{IPs: ips, Role: role, EveryoneRole: everyone})
}

func AdminAccountFromModelWithOptions(cfg config.Config, account models.Account, options AdminAccountOptions) AdminAccount {
	user := account.User
	hasUser := user.ID != 0
	ipItems := make([]AdminAccountIP, 0, len(options.IPs))
	ipItems = append(ipItems, options.IPs...)
	var firstIP *string
	if len(ipItems) > 0 {
		firstIP = &ipItems[0].IP
	}
	var rolePayload any
	if options.Role != nil {
		rolePayload = RoleFromModel(*options.Role, options.EveryoneRole)
	}
	var email *string
	var locale *string
	if hasUser {
		email = &user.Email
		locale = stringPtr(user.Locale)
	}
	return AdminAccount{
		ID:                     strconv.FormatInt(account.ID, 10),
		Username:               account.Username,
		Domain:                 stringPtr(account.Domain),
		CreatedAt:              restTimestamp(account.CreatedAt),
		Email:                  email,
		IP:                     firstIP,
		Confirmed:              boolPtrIf(hasUser, user.ConfirmedAt.Valid),
		Suspended:              account.SuspendedAt.Valid,
		Silenced:               account.SilencedAt.Valid,
		Sensitized:             account.SensitizedAt.Valid,
		Disabled:               boolPtrIf(hasUser, user.Disabled),
		Approved:               boolPtrIf(hasUser, user.Approved),
		Locale:                 locale,
		InviteRequest:          options.InviteRequest,
		CreatedByApplicationID: idPtr(user.CreatedByApplicationID),
		InvitedByAccountID:     options.InvitedByAccountID,
		IPs:                    ipItems,
		Account:                AccountFromModel(cfg, account),
		Role:                   rolePayload,
	}
}

func AdminReportFromModel(cfg config.Config, report models.Report, statuses []models.Status) AdminReport {
	var assigned *AdminAccount
	if report.AssignedAccount.ID != 0 {
		account := AdminAccountFromModel(cfg, report.AssignedAccount)
		assigned = &account
	}
	var actionTakenBy *AdminAccount
	if report.ActionTakenByAccount.ID != 0 {
		account := AdminAccountFromModel(cfg, report.ActionTakenByAccount)
		actionTakenBy = &account
	}
	return AdminReportFromModelWithAdminAccounts(
		cfg,
		report,
		statuses,
		AdminAccountFromModel(cfg, report.Account),
		AdminAccountFromModel(cfg, report.TargetAccount),
		assigned,
		actionTakenBy,
		nil,
	)
}

func AdminReportFromModelWithAdminAccounts(cfg config.Config, report models.Report, statuses []models.Status, account AdminAccount, targetAccount AdminAccount, assignedAccount *AdminAccount, actionTakenByAccount *AdminAccount, rules []models.Rule) AdminReport {
	return AdminReportFromModelWithAdminAccountsAndCurrent(cfg, report, statuses, account, targetAccount, assignedAccount, actionTakenByAccount, rules, nil)
}

func AdminReportFromModelWithAdminAccountsAndCurrent(cfg config.Config, report models.Report, statuses []models.Status, account AdminAccount, targetAccount AdminAccount, assignedAccount *AdminAccount, actionTakenByAccount *AdminAccount, rules []models.Rule, currentAccount *models.Account) AdminReport {
	out := AdminReport{
		ID:                   strconv.FormatInt(report.ID, 10),
		ActionTaken:          report.ActionTakenAt.Valid,
		ActionTakenAt:        timePtr(report.ActionTakenAt),
		Category:             reportCategoryName(report.Category),
		Comment:              report.Comment,
		Forwarded:            boolPtr(report.Forwarded),
		CreatedAt:            restTimestamp(report.CreatedAt),
		UpdatedAt:            restTimestamp(report.UpdatedAt),
		Account:              account,
		TargetAccount:        targetAccount,
		AssignedAccount:      assignedAccount,
		ActionTakenByAccount: actionTakenByAccount,
		Statuses:             make([]Status, 0, len(statuses)),
		Rules:                InstanceRulesFromModels(rules),
	}
	for _, status := range statuses {
		out.Statuses = append(out.Statuses, StatusFromModel(cfg, status, currentAccount))
	}
	return out
}

func AdminDomainAllowFromModel(allow models.DomainAllow) AdminDomainAllow {
	return AdminDomainAllow{
		ID:        strconv.FormatInt(allow.ID, 10),
		Domain:    allow.Domain,
		CreatedAt: restTimestamp(allow.CreatedAt),
	}
}

func AdminDomainBlockFromModel(block models.DomainBlock) AdminDomainBlock {
	hash := sha256.Sum256([]byte(block.Domain))
	return AdminDomainBlock{
		ID:             strconv.FormatInt(block.ID, 10),
		Domain:         block.Domain,
		Digest:         hex.EncodeToString(hash[:]),
		CreatedAt:      restTimestamp(block.CreatedAt),
		Severity:       domainBlockSeverity(block.Severity),
		RejectMedia:    block.RejectMedia,
		RejectReports:  block.RejectReports,
		PrivateComment: stringPtr(block.PrivateComment),
		PublicComment:  stringPtr(block.PublicComment),
		Obfuscate:      block.Obfuscate,
	}
}

func AdminExistingDomainBlockErrorFromModel(block models.DomainBlock) AdminExistingDomainBlockError {
	return AdminExistingDomainBlockErrorFromModelWithMessage(block, "You have already imposed stricter limits on "+block.Domain+".")
}

func AdminExistingDomainBlockErrorFromModelWithMessage(block models.DomainBlock, message string) AdminExistingDomainBlockError {
	return AdminExistingDomainBlockError{
		Error:               message,
		ExistingDomainBlock: AdminDomainBlockFromModel(block),
	}
}

func AdminEmailDomainBlockFromModel(block models.EmailDomainBlock) AdminEmailDomainBlock {
	return AdminEmailDomainBlockFromModelWithHistory(block, nil)
}

func AdminEmailDomainBlockFromModelWithHistory(block models.EmailDomainBlock, history []AdminEmailDomainBlockHistory) AdminEmailDomainBlock {
	if history == nil {
		history = []AdminEmailDomainBlockHistory{}
	}
	return AdminEmailDomainBlock{
		ID:                strconv.FormatInt(block.ID, 10),
		Domain:            block.Domain,
		CreatedAt:         restTimestamp(block.CreatedAt),
		History:           history,
		AllowWithApproval: block.AllowWithApproval,
	}
}

func AdminCanonicalEmailBlockFromModel(block models.CanonicalEmailBlock) AdminCanonicalEmailBlock {
	return AdminCanonicalEmailBlock{
		ID:                 strconv.FormatInt(block.ID, 10),
		CanonicalEmailHash: block.CanonicalEmailHash,
	}
}

func AdminIPBlockFromModel(block models.IPBlock) AdminIPBlock {
	return AdminIPBlock{
		ID:        strconv.FormatInt(block.ID, 10),
		IP:        adminIPBlockAddress(block.IP),
		Severity:  ipBlockSeverity(block.Severity),
		Comment:   block.Comment,
		CreatedAt: restTimestamp(block.CreatedAt),
		ExpiresAt: timePtr(block.ExpiresAt),
	}
}

func adminIPBlockAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "/") {
		return value
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return value
	}
	if ip.To4() != nil {
		return value + "/32"
	}
	return value + "/128"
}

func InitialStateFromConfig(cfg config.Config, current *models.Account, token string) InitialState {
	return InitialStateFromConfigWithComposeText(cfg, current, token, "")
}

func InitialStateFromConfigWithComposeText(cfg config.Config, current *models.Account, token string, composeText string) InitialState {
	return InitialStateFromConfigWithOptions(cfg, current, token, InitialStateOptions{ComposeText: composeText})
}

func InitialStateFromConfigWithOptions(cfg config.Config, current *models.Account, token string, options InitialStateOptions) InitialState {
	serverSettings := DefaultInitialStateServerSettings()
	if options.ServerSettings != nil {
		serverSettings = *options.ServerSettings
	}
	siteTitle := options.SiteTitle
	if !options.SiteTitleSet && siteTitle == "" {
		siteTitle = cfg.Title
	}

	meta := map[string]any{
		"streaming_api_base_url":   cfg.StreamingBaseURL(),
		"access_token":             token,
		"locale":                   cfg.Locale(),
		"domain":                   unicodeDomain(cfg.LocalDomain),
		"title":                    siteTitle,
		"admin":                    nil,
		"search_enabled":           cfg.MeiliEnabled,
		"repository":               cfg.Repository,
		"source_url":               cfg.SourceURL,
		"version":                  instanceVersion(cfg),
		"actual_version":           cfg.Version,
		"limited_federation_mode":  cfg.LimitedFederationMode,
		"mascot":                   optionalStringAny(options.MascotURL),
		"profile_directory":        serverSettings.ProfileDirectory,
		"trends_enabled":           serverSettings.TrendsEnabled,
		"registrations_open":       options.RegistrationsOpen,
		"timeline_preview":         serverSettings.TimelinePreview,
		"activity_api_enabled":     serverSettings.ActivityAPIEnabled,
		"single_user_mode":         cfg.SingleUserMode,
		"trends_as_landing_page":   serverSettings.TrendsAsLandingPage,
		"landing_page":             serverSettings.LandingPage,
		"local_live_feed_access":   serverSettings.LocalLiveFeedAccess,
		"remote_live_feed_access":  serverSettings.RemoteLiveFeedAccess,
		"local_topic_feed_access":  serverSettings.LocalTopicFeedAccess,
		"remote_topic_feed_access": serverSettings.RemoteTopicFeedAccess,
		"status_page_url":          serverSettings.StatusPageURL,
		"sso_redirect":             optionalStringAny(cfg.SSORedirect),
		"terms_of_service_enabled": options.TermsOfServiceEnabled,
		"auto_play_gif":            serverSettings.AutoPlayGIF,
		"display_media":            serverSettings.DisplayMedia,
		"reduce_motion":            serverSettings.ReduceMotion,
		"use_blurhash":             serverSettings.UseBlurhash,
		"crop_images":              serverSettings.CropImages,
	}

	accounts := map[string]Account{}
	if options.AdminAccount != nil {
		id := strconv.FormatInt(options.AdminAccount.ID, 10)
		meta["admin"] = id
		accounts[id] = AccountFromModel(cfg, *options.AdminAccount)
	}
	if cfg.SingleUserMode && options.OwnerAccount != nil {
		id := strconv.FormatInt(options.OwnerAccount.ID, 10)
		meta["owner"] = id
		accounts[id] = AccountFromModel(cfg, *options.OwnerAccount)
	}
	if current != nil {
		id := strconv.FormatInt(current.ID, 10)
		meta["me"] = id
		if current.MovedToAccountID.Valid {
			movedID := strconv.FormatInt(current.MovedToAccountID.Int64, 10)
			meta["moved_to_account_id"] = movedID
			if options.MovedToAccount != nil {
				accounts[movedID] = AccountFromModel(cfg, *options.MovedToAccount)
			}
		}
		settings := map[string]any{}
		if options.User != nil {
			settings = userSettings(*options.User)
		}
		applyAuthenticatedMetaSettings(meta, settings)
		accounts[id] = AccountFromModel(cfg, *current)
	}
	if options.DisabledAccount != nil {
		id := strconv.FormatInt(options.DisabledAccount.ID, 10)
		meta["disabled_account_id"] = id
		accounts[id] = AccountFromModel(cfg, *options.DisabledAccount)
		if options.DisabledAccount.MovedToAccountID.Valid {
			movedID := strconv.FormatInt(options.DisabledAccount.MovedToAccountID.Int64, 10)
			meta["moved_to_account_id"] = movedID
			if options.MovedToAccount != nil {
				accounts[movedID] = AccountFromModel(cfg, *options.MovedToAccount)
			}
		}
	}

	settings := options.Settings
	if settings == nil {
		settings = map[string]any{}
	}

	compose := map[string]any{"text": options.ComposeText}
	if current != nil && options.User != nil {
		settings := userSettings(*options.User)
		compose["me"] = strconv.FormatInt(current.ID, 10)
		compose["default_privacy"] = UserDefaultPrivacy(settings, *current)
		compose["default_sensitive"] = boolSetting(settings, "default_sensitive", false)
		compose["default_language"] = preferredPostingLanguage(settings, *options.User, cfg)
		compose["default_quote_policy"] = stringSetting(settings, "default_quote_policy", "public")
	}
	if options.ComposeVisibility != "" {
		compose["default_privacy"] = options.ComposeVisibility
	}

	var role *Role
	if options.Role != nil {
		role = RoleFromModel(*options.Role, options.EveryoneRole)
	}
	var pushSubscription *WebPushSubscription
	if options.PushSubscription != nil && options.PushSubscription.ID != 0 {
		serialized := WebPushSubscriptionFromModel(cfg, *options.PushSubscription)
		pushSubscription = &serialized
	}

	return InitialState{
		Meta:             meta,
		Compose:          compose,
		Accounts:         accounts,
		PushSubscription: pushSubscription,
		MediaAttachments: map[string]any{
			"accept_content_types": mediaAttachmentAcceptContentTypes(),
		},
		Settings:               settings,
		Languages:              SupportedLanguageRows(),
		Features:               append([]string{}, cfg.ExperimentalFeatures...),
		CriticalUpdatesPending: options.CriticalUpdatesPending,
		Role:                   role,
	}
}

func mediaAttachmentAcceptContentTypes() []string {
	out := make([]string, 0, len(mediaAttachmentFileExtensions)+len(mediaAttachmentMimeTypes))
	out = append(out, mediaAttachmentFileExtensions...)
	out = append(out, mediaAttachmentMimeTypes...)
	return out
}

func RoleFromModel(role models.UserRole, everyone *models.UserRole) *Role {
	return &Role{
		ID:          strconv.FormatInt(role.ID, 10),
		Name:        role.Name,
		Permissions: strconv.FormatInt(computedRolePermissions(role, everyone), 10),
		Color:       role.Color,
		Highlighted: role.Highlighted,
	}
}

const (
	roleIDEveryone       = int64(-99)
	rolePermissionAdmin  = int64(1 << 0)
	rolePermissionInvite = int64(1 << 16)
	rolePermissionsAll   = int64((1 << 21) - 1)
)

func computedRolePermissions(role models.UserRole, everyone *models.UserRole) int64 {
	permissions := role.Permissions
	if role.ID != roleIDEveryone {
		everyonePermissions := rolePermissionInvite
		if everyone != nil {
			everyonePermissions = everyone.Permissions
		}
		permissions |= everyonePermissions
	}
	if permissions&rolePermissionAdmin == rolePermissionAdmin {
		return rolePermissionsAll
	}
	return permissions
}

func unicodeDomain(domain string) string {
	if domain == "" {
		return ""
	}
	labels := strings.Split(domain, ".")
	changed := false
	for i, label := range labels {
		decoded, ok := decodePunycodeLabel(label)
		if ok {
			labels[i] = decoded
			changed = true
		}
	}
	if !changed {
		return domain
	}
	return strings.Join(labels, ".")
}

func decodePunycodeLabel(label string) (string, bool) {
	const prefix = "xn--"
	if !strings.HasPrefix(strings.ToLower(label), prefix) {
		return "", false
	}
	decoded, ok := decodePunycode(strings.TrimPrefix(strings.ToLower(label), prefix))
	if !ok || decoded == "" {
		return "", false
	}
	return decoded, true
}

func decodePunycode(input string) (string, bool) {
	const (
		base        = 36
		tMin        = 1
		tMax        = 26
		skew        = 38
		damp        = 700
		initialBias = 72
		initialN    = 128
	)
	n, i, bias := initialN, 0, initialBias
	output := []rune{}
	if delimiter := strings.LastIndex(input, "-"); delimiter >= 0 {
		for _, r := range input[:delimiter] {
			if r >= 0x80 {
				return "", false
			}
			output = append(output, r)
		}
		input = input[delimiter+1:]
	}
	first := len(output) == 0
	for pos := 0; pos < len(input); {
		oldI := i
		w := 1
		for k := base; ; k += base {
			if pos >= len(input) {
				return "", false
			}
			digit, ok := punycodeDigit(input[pos])
			if !ok {
				return "", false
			}
			pos++
			if digit > (int(^uint(0)>>1)-i)/w {
				return "", false
			}
			i += digit * w
			t := k - bias
			if t < tMin {
				t = tMin
			} else if t > tMax {
				t = tMax
			}
			if digit < t {
				break
			}
			if w > int(^uint(0)>>1)/(base-t) {
				return "", false
			}
			w *= base - t
		}
		outLen := len(output) + 1
		bias = adaptPunycodeBias(i-oldI, outLen, first, damp, base, tMin, tMax, skew)
		first = false
		n += i / outLen
		insert := i % outLen
		if n > 0x10ffff {
			return "", false
		}
		output = append(output, 0)
		copy(output[insert+1:], output[insert:])
		output[insert] = rune(n)
		i = insert + 1
	}
	return string(output), true
}

func punycodeDigit(b byte) (int, bool) {
	switch {
	case b >= 'a' && b <= 'z':
		return int(b - 'a'), true
	case b >= '0' && b <= '9':
		return int(b-'0') + 26, true
	default:
		return 0, false
	}
}

func adaptPunycodeBias(delta int, numPoints int, first bool, damp int, base int, tMin int, tMax int, skew int) int {
	if first {
		delta /= damp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((base-tMin)*tMax)/2 {
		delta /= base - tMin
		k += base
	}
	return k + ((base-tMin+1)*delta)/(delta+skew)
}

func applyAuthenticatedMetaSettings(meta map[string]any, settings map[string]any) {
	applyInitialStateMetaDefaults(meta)
	meta["disable_hover_cards"] = boolSetting(settings, "web.disable_hover_cards", metaBoolDefault(meta, "disable_hover_cards", false))
	meta["boost_modal"] = boolSetting(settings, "web.reblog_modal", metaBoolDefault(meta, "boost_modal", false))
	meta["delete_modal"] = boolSetting(settings, "web.delete_modal", metaBoolDefault(meta, "delete_modal", true))
	meta["missing_alt_text_modal"] = boolSetting(settings, "web.missing_alt_text_modal", metaBoolDefault(meta, "missing_alt_text_modal", true))
	meta["auto_play_gif"] = boolSetting(settings, "web.auto_play", false)
	meta["emoji_style"] = stringSetting(settings, "web.emoji_style", "auto")
	meta["display_media"] = stringSetting(settings, "web.display_media", "default")
	meta["expand_spoilers"] = boolSetting(settings, "web.expand_content_warnings", metaBoolDefault(meta, "expand_spoilers", false))
	meta["reduce_motion"] = boolSetting(settings, "web.reduce_motion", false)
	meta["disable_swiping"] = boolSetting(settings, "web.disable_swiping", metaBoolDefault(meta, "disable_swiping", false))
	meta["advanced_layout"] = boolSetting(settings, "web.advanced_layout", metaBoolDefault(meta, "advanced_layout", false))
	meta["use_blurhash"] = boolSetting(settings, "web.use_blurhash", true)
	meta["use_pending_items"] = boolSetting(settings, "web.use_pending_items", metaBoolDefault(meta, "use_pending_items", false))
	trendsEnabled := metaBoolDefault(meta, "trends_enabled", true)
	meta["show_trends"] = trendsEnabled && boolSetting(settings, "web.trends", metaBoolDefault(meta, "show_trends", true))
	meta["crop_images"] = boolSetting(settings, "web.crop_images", true)
}

func applyInitialStateMetaDefaults(meta map[string]any) {
	meta["disable_hover_cards"] = metaBoolDefault(meta, "disable_hover_cards", false)
	meta["boost_modal"] = metaBoolDefault(meta, "boost_modal", false)
	meta["delete_modal"] = metaBoolDefault(meta, "delete_modal", true)
	meta["missing_alt_text_modal"] = metaBoolDefault(meta, "missing_alt_text_modal", true)
	meta["expand_spoilers"] = metaBoolDefault(meta, "expand_spoilers", false)
	meta["disable_swiping"] = metaBoolDefault(meta, "disable_swiping", false)
	meta["advanced_layout"] = metaBoolDefault(meta, "advanced_layout", false)
	meta["use_pending_items"] = metaBoolDefault(meta, "use_pending_items", false)
	meta["show_trends"] = metaBoolDefault(meta, "show_trends", true)
}

func metaBoolDefault(meta map[string]any, key string, fallback bool) bool {
	if value, ok := meta[key].(bool); ok {
		return value
	}
	return fallback
}

func preferredPostingLanguage(settings map[string]any, user models.User, cfg config.Config) string {
	if value := stringSetting(settings, "default_language", ""); value != "" {
		return value
	}
	if user.Locale.Valid && strings.TrimSpace(user.Locale.String) != "" {
		return strings.TrimSpace(user.Locale.String)
	}
	return cfg.Locale()
}

func formatHTML(value string) string {
	escaped := html.EscapeString(value)
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	escaped = strings.ReplaceAll(escaped, "\n\n", "</p><p>")
	escaped = strings.ReplaceAll(escaped, "\n", "<br />")
	return "<p>" + escaped + "</p>"
}

func accountBioHTML(cfg config.Config, account models.Account) string {
	status := models.Status{
		Text:    account.Note,
		Local:   sql.NullBool{Bool: account.Local(), Valid: true},
		Account: account,
	}
	return statusContentHTML(cfg, status)
}

func accountFieldValueHTML(cfg config.Config, account models.Account, field Field) string {
	if field.VerifiedAt != nil && !account.Local() {
		if value := accountRemoteVerifiedFieldURL(field.Value); value != "" {
			return statusShortURLLink(value)
		}
	}
	if !account.Local() {
		return sanitizeStatusContentHTML(field.Value)
	}
	value := strings.ReplaceAll(strings.ReplaceAll(field.Value, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(statusLinkifyInlineWithRelMe(cfg, value, statusMentionResolver{}, true), "\n", "<br />")
}

func AccountFieldValueHTML(cfg config.Config, account models.Account, field Field) string {
	return accountFieldValueHTML(cfg, account, field)
}

func accountRemoteVerifiedFieldURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	matches := previewCardOEmbedHTMLTagPattern.FindAllStringSubmatchIndex(trimmed, -1)
	if len(matches) != 2 {
		return ""
	}
	open := matches[0]
	close := matches[1]
	if trimmed[:open[0]] != "" || strings.TrimSpace(trimmed[open[1]:close[0]]) == "" || strings.TrimSpace(trimmed[close[1]:]) != "" {
		return ""
	}
	if trimmed[open[2]:open[3]] != "" || !strings.EqualFold(trimmed[open[4]:open[5]], "a") || open[6] < 0 {
		return ""
	}
	if trimmed[close[2]:close[3]] == "" || !strings.EqualFold(trimmed[close[4]:close[5]], "a") {
		return ""
	}
	href := ""
	for _, attr := range previewCardOEmbedAttrPattern.FindAllStringSubmatch(trimmed[open[6]:open[7]], -1) {
		if len(attr) >= 5 && strings.EqualFold(attr[1], "href") {
			href = strings.TrimSpace(firstNonEmptyString(attr[2], attr[3], attr[4]))
			break
		}
	}
	text := strings.TrimSpace(trimmed[open[1]:close[0]])
	if href == "" || href != text || (!strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://")) {
		return ""
	}
	return href
}

func statusContentHTML(cfg config.Config, status models.Status) string {
	if strings.TrimSpace(status.Text) == "" {
		return ""
	}
	if !statusLocal(status) {
		return sanitizeStatusContentHTML(restoreStatusCustomEmojiShortcodes(status.Text, status.CustomEmojis))
	}
	paragraphs := strings.Split(strings.ReplaceAll(strings.ReplaceAll(status.Text, "\r\n", "\n"), "\r", "\n"), "\n\n")
	mentions := newStatusMentionResolver(status.Mentions)
	var out strings.Builder
	for _, paragraph := range paragraphs {
		paragraph = strings.Trim(paragraph, "\n")
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		out.WriteString("<p>")
		out.WriteString(strings.ReplaceAll(statusLinkifyInline(cfg, paragraph, mentions), "\n", "<br />"))
		out.WriteString("</p>")
	}
	return out.String()
}

func restoreStatusCustomEmojiShortcodes(value string, emojis []models.CustomEmoji) string {
	if len(emojis) == 0 || !strings.Contains(strings.ToLower(value), "<img") {
		return value
	}
	allowed := make(map[string]struct{}, len(emojis))
	for _, emoji := range emojis {
		if shortcode := strings.TrimSpace(emoji.Shortcode); shortcode != "" {
			allowed[shortcode] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return value
	}
	var out strings.Builder
	last := 0
	for _, match := range previewCardOEmbedHTMLTagPattern.FindAllStringSubmatchIndex(value, -1) {
		if value[match[2]:match[3]] != "" || !strings.EqualFold(value[match[4]:match[5]], "img") || match[6] < 0 {
			continue
		}
		shortcode := legacyStatusCustomEmojiShortcode(value[match[6]:match[7]], allowed)
		if shortcode == "" {
			continue
		}
		out.WriteString(value[last:match[0]])
		out.WriteString(":" + shortcode + ":")
		last = match[1]
	}
	if last == 0 {
		return value
	}
	out.WriteString(value[last:])
	return out.String()
}

func legacyStatusCustomEmojiShortcode(attrText string, allowed map[string]struct{}) string {
	hasEmojiClass := false
	shortcode := ""
	for _, match := range previewCardOEmbedAttrPattern.FindAllStringSubmatch(attrText, -1) {
		if len(match) < 5 {
			continue
		}
		key := strings.ToLower(match[1])
		value := strings.TrimSpace(firstNonEmptyString(match[2], match[3], match[4]))
		switch key {
		case "class":
			hasEmojiClass = statusContentAllowedImageClasses(value) == "emojione"
		case "alt":
			if len(value) >= 3 && strings.HasPrefix(value, ":") && strings.HasSuffix(value, ":") {
				shortcode = value[1 : len(value)-1]
			}
		}
	}
	if !hasEmojiClass {
		return ""
	}
	if _, ok := allowed[shortcode]; !ok {
		return ""
	}
	return shortcode
}

func StatusContentHTML(cfg config.Config, status models.Status) string {
	return statusContentHTML(cfg, status)
}

func sanitizeStatusContentHTML(value string) string {
	value = previewCardOEmbedScriptBlockPattern.ReplaceAllString(value, "")
	var out strings.Builder
	last := 0
	suppressedAnchors := 0
	for _, match := range previewCardOEmbedHTMLTagPattern.FindAllStringSubmatchIndex(value, -1) {
		if match[0] > last {
			out.WriteString(html.EscapeString(value[last:match[0]]))
		}
		closing := value[match[2]:match[3]] != ""
		name := strings.ToLower(value[match[4]:match[5]])
		attrText := ""
		if match[6] >= 0 {
			attrText = value[match[6]:match[7]]
		}
		if name == "a" {
			if closing && suppressedAnchors > 0 {
				suppressedAnchors--
				last = match[1]
				continue
			}
			if !closing && statusContentAnchorHrefUnsupported(attrText) {
				suppressedAnchors++
				last = match[1]
				continue
			}
		}
		if name == "img" && (closing || !statusContentCustomEmojiImage(attrText)) {
			last = match[1]
			continue
		}
		if statusContentAllowedElement(name) {
			if closing {
				if name != "br" {
					out.WriteString("</" + name + ">")
				}
			} else {
				out.WriteString("<" + name + sanitizeStatusContentAttrs(name, attrText) + ">")
			}
		} else if statusContentHeadingElement(name) {
			if closing {
				out.WriteString("</strong></p>")
			} else {
				out.WriteString("<p><strong>")
			}
		}
		last = match[1]
	}
	if last < len(value) {
		out.WriteString(html.EscapeString(value[last:]))
	}
	return strings.TrimSpace(out.String())
}

func statusContentAllowedElement(name string) bool {
	switch name {
	case "p", "br", "span", "a", "del", "s", "pre", "blockquote", "code", "b", "strong", "u", "i", "em", "ul", "ol", "li", "img":
		return true
	default:
		return false
	}
}

func statusContentHeadingElement(name string) bool {
	switch name {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return true
	default:
		return false
	}
}

func sanitizeStatusContentAttrs(name string, attrText string) string {
	attrs := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, match := range previewCardOEmbedAttrPattern.FindAllStringSubmatch(attrText, -1) {
		if len(match) < 5 {
			continue
		}
		key := strings.ToLower(match[1])
		if name == "a" && key == "rel" {
			continue
		}
		if !statusContentAllowedAttr(name, key) {
			continue
		}
		value := strings.TrimSpace(firstNonEmptyString(match[2], match[3], match[4]))
		if key == "href" && !statusContentLinkProtocolAllowed(value) {
			continue
		}
		if name == "img" && key == "src" && !statusContentImageSrcAllowed(value) {
			continue
		}
		if key == "class" {
			if name == "img" {
				value = statusContentAllowedImageClasses(value)
			} else {
				value = statusContentAllowedClasses(value)
			}
			if value == "" {
				continue
			}
		}
		if name == "img" && key == "draggable" && value != "false" {
			continue
		}
		if key == "translate" && value != "no" {
			continue
		}
		attrs = append(attrs, key+`="`+html.EscapeString(value)+`"`)
		seen[key] = struct{}{}
	}
	for _, token := range strings.Fields(attrText) {
		key := strings.ToLower(strings.Trim(token, "/"))
		if key == "" || strings.Contains(key, "=") || !statusContentAllowedAttr(name, key) || key != "reversed" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		attrs = append(attrs, key+`=""`)
		seen[key] = struct{}{}
	}
	if name == "a" {
		attrs = append(attrs, `rel="nofollow noopener noreferrer"`, `target="_blank"`)
	}
	if len(attrs) == 0 {
		return ""
	}
	return " " + strings.Join(attrs, " ")
}

func statusContentAllowedAttr(name string, key string) bool {
	switch name {
	case "a":
		return key == "href" || key == "rel" || key == "class" || key == "translate"
	case "span":
		return key == "class" || key == "translate"
	case "img":
		return key == "src" || key == "alt" || key == "title" || key == "class" || key == "draggable"
	case "ol":
		return key == "start" || key == "reversed"
	case "li":
		return key == "value"
	default:
		return false
	}
}

func statusContentAllowedClasses(raw string) string {
	classes := make([]string, 0, 2)
	for _, class := range strings.Fields(raw) {
		switch {
		case strings.HasPrefix(class, "h-"), strings.HasPrefix(class, "p-"), strings.HasPrefix(class, "u-"), strings.HasPrefix(class, "dt-"), strings.HasPrefix(class, "e-"):
			classes = append(classes, class)
		case class == "mention" || class == "hashtag" || class == "ellipsis" || class == "invisible":
			classes = append(classes, class)
		}
	}
	return strings.Join(classes, " ")
}

func statusContentAllowedImageClasses(raw string) string {
	for _, class := range strings.Fields(raw) {
		if class == "emojione" {
			return class
		}
	}
	return ""
}

func statusContentCustomEmojiImage(attrText string) bool {
	hasEmojiClass := false
	hasSafeSrc := false
	for _, match := range previewCardOEmbedAttrPattern.FindAllStringSubmatch(attrText, -1) {
		if len(match) < 5 {
			continue
		}
		key := strings.ToLower(match[1])
		value := strings.TrimSpace(firstNonEmptyString(match[2], match[3], match[4]))
		switch key {
		case "class":
			hasEmojiClass = statusContentAllowedImageClasses(value) == "emojione"
		case "src":
			hasSafeSrc = statusContentImageSrcAllowed(value)
		}
	}
	return hasEmojiClass && hasSafeSrc
}

func statusContentImageSrcAllowed(raw string) bool {
	value := strings.TrimSpace(raw)
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return true
	default:
		return false
	}
}

func statusContentAnchorHrefUnsupported(attrText string) bool {
	for _, match := range previewCardOEmbedAttrPattern.FindAllStringSubmatch(attrText, -1) {
		if len(match) < 5 || !strings.EqualFold(match[1], "href") {
			continue
		}
		value := strings.TrimSpace(firstNonEmptyString(match[2], match[3], match[4]))
		return !statusContentLinkProtocolAllowed(value)
	}
	return false
}

func statusContentLinkProtocolAllowed(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "dat", "dweb", "ipfs", "ipns", "ssb", "gopher", "xmpp", "magnet", "gemini":
		return true
	default:
		return false
	}
}

type statusMentionResolver struct {
	accounts []models.Account
}

func newStatusMentionResolver(mentions []models.Mention) statusMentionResolver {
	resolver := statusMentionResolver{accounts: make([]models.Account, 0, len(mentions))}
	for _, mention := range mentions {
		account := mention.Account
		if account.ID == 0 || strings.TrimSpace(account.Username) == "" {
			continue
		}
		resolver.accounts = append(resolver.accounts, account)
	}
	return resolver
}

func (r statusMentionResolver) resolve(cfg config.Config, acct string) (models.Account, string, bool) {
	username, domain, _ := strings.Cut(strings.TrimSpace(acct), "@")
	username = strings.TrimSpace(username)
	domain = strings.TrimSpace(domain)
	if username == "" {
		return models.Account{}, "", false
	}
	if statusMentionLocalDomain(cfg, domain) {
		domain = ""
	}
	var found models.Account
	foundOK := false
	sameUsernameHits := 0
	for _, account := range r.accounts {
		if !strings.EqualFold(account.Username, username) {
			continue
		}
		if statusMentionSameDomain(account, domain) {
			found = account
			foundOK = true
			continue
		}
		sameUsernameHits++
	}
	if !foundOK {
		return models.Account{}, "", false
	}
	display := found.Username
	if sameUsernameHits > 0 {
		display = found.Acct()
	}
	return found, display, true
}

func statusMentionSameDomain(account models.Account, domain string) bool {
	if account.Domain.Valid && account.Domain.String != "" {
		return strings.EqualFold(account.Domain.String, domain)
	}
	return domain == ""
}

func statusMentionLocalDomain(cfg config.Config, domain string) bool {
	return domain != "" && (strings.EqualFold(domain, cfg.LocalDomain) || strings.EqualFold(domain, cfg.WebDomain))
}

func statusLinkifyInline(cfg config.Config, text string, mentions statusMentionResolver) string {
	return statusLinkifyInlineWithRelMe(cfg, text, mentions, false)
}

func statusLinkifyInlineWithRelMe(cfg config.Config, text string, mentions statusMentionResolver, relMe bool) string {
	var out strings.Builder
	last := 0
	matches := statusLinkPattern.FindAllStringIndex(text, -1)
	for _, match := range matches {
		start, end := match[0], match[1]
		raw := text[start:end]
		if !mastodonLinkTokenBoundaryOK(text, start-1, raw) {
			continue
		}
		token, trailing := trimTrailingLinkPunctuation(raw)
		if token == "" {
			continue
		}
		out.WriteString(html.EscapeString(text[last:start]))
		out.WriteString(statusLinkHTMLWithRelMe(cfg, token, mentions, relMe))
		out.WriteString(html.EscapeString(trailing))
		last = end
	}
	out.WriteString(html.EscapeString(text[last:]))
	return out.String()
}

func statusLinkHTML(cfg config.Config, token string, mentions statusMentionResolver) string {
	return statusLinkHTMLWithRelMe(cfg, token, mentions, false)
}

func statusLinkHTMLWithRelMe(cfg config.Config, token string, mentions statusMentionResolver, relMe bool) string {
	switch {
	case statusLinkTokenIsURL(token):
		if !statusLinkURLValid(token) {
			return html.EscapeString(token)
		}
		if strings.Contains(token, "komiflo.com") {
			return statusFullURLLinkWithRelMe(token, relMe)
		}
		return statusShortURLLinkWithRelMe(token, relMe)
	case strings.HasPrefix(token, "#") || strings.HasPrefix(token, "＃"):
		prefix := "#"
		if strings.HasPrefix(token, "＃") {
			prefix = "＃"
		}
		tag := strings.TrimPrefix(strings.TrimPrefix(token, "#"), "＃")
		if !mastodonHashtagNameValid(tag) {
			return html.EscapeString(token)
		}
		return `<a href="` + html.EscapeString(cfg.BaseURL()+"/tags/"+url.PathEscape(strings.ToLower(tag))) + `" class="mention hashtag" rel="tag">` + prefix + `<span>` + html.EscapeString(tag) + `</span></a>`
	case strings.HasPrefix(token, "@"):
		acct := strings.TrimPrefix(token, "@")
		account, display, ok := mentions.resolve(cfg, acct)
		if !ok {
			return html.EscapeString(token)
		}
		return `<span class="h-card" translate="no"><a href="` + html.EscapeString(accountURL(cfg, account)) + `" class="u-url mention">@<span>` + html.EscapeString(display) + `</span></a></span>`
	default:
		return html.EscapeString(token)
	}
}

func statusLinkTokenIsURL(token string) bool {
	return strings.HasPrefix(token, "http://") ||
		strings.HasPrefix(token, "https://") ||
		strings.HasPrefix(token, "dat://") ||
		strings.HasPrefix(token, "dweb://") ||
		strings.HasPrefix(token, "ipfs://") ||
		strings.HasPrefix(token, "ipns://") ||
		strings.HasPrefix(token, "ssb://") ||
		strings.HasPrefix(token, "gopher://") ||
		strings.HasPrefix(token, "gemini://") ||
		strings.HasPrefix(token, "xmpp:") ||
		strings.HasPrefix(token, "magnet:?")
}

func statusLinkURLValid(token string) bool {
	if strings.Contains(token, `\`) {
		return false
	}
	parsed, err := url.Parse(token)
	return err == nil && parsed.Scheme != "" && (parsed.Host != "" || strings.HasPrefix(token, "xmpp:") || strings.HasPrefix(token, "magnet:?"))
}

func statusShortURLLink(raw string) string {
	return statusShortURLLinkWithRelMe(raw, false)
}

func statusShortURLLinkWithRelMe(raw string, relMe bool) string {
	prefix := statusURLPrefix(raw)
	rest := raw[len(prefix):]
	display := rest
	suffix := ""
	ellipsisClass := ""
	if len([]rune(rest)) > 30 {
		runes := []rune(rest)
		display = string(runes[:30])
		suffix = string(runes[30:])
		ellipsisClass = ` class="ellipsis"`
	}
	return `<a href="` + html.EscapeString(raw) + `" target="_blank" rel="` + statusURLRel(relMe) + `" translate="no"><span class="invisible">` + html.EscapeString(prefix) + `</span><span` + ellipsisClass + `>` + html.EscapeString(display) + `</span><span class="invisible">` + html.EscapeString(suffix) + `</span></a>`
}

func statusFullURLLink(raw string) string {
	return statusFullURLLinkWithRelMe(raw, false)
}

func statusFullURLLinkWithRelMe(raw string, relMe bool) string {
	return `<a href="` + html.EscapeString(raw) + `" target="_blank" rel="` + statusURLRel(relMe) + `" translate="no">` + html.EscapeString(raw) + `</a>`
}

func statusURLRel(relMe bool) string {
	if relMe {
		return "nofollow noopener noreferrer me"
	}
	return "nofollow noopener noreferrer"
}

func statusURLPrefix(raw string) string {
	for _, prefix := range []string{"https://www.", "http://www.", "https://", "http://", "xmpp:"} {
		if strings.HasPrefix(raw, prefix) {
			return prefix
		}
	}
	return ""
}

func accountURL(cfg config.Config, account models.Account) string {
	if account.URL.Valid && account.URL.String != "" {
		return account.URL.String
	}
	if account.Local() {
		return cfg.BaseURL() + "/@" + url.PathEscape(account.Username)
	}
	return accountURI(cfg, account)
}

func accountURI(cfg config.Config, account models.Account) string {
	if account.Local() && account.ID > 0 && account.IDScheme.Valid && account.IDScheme.Int64 == 1 {
		return cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(account.ID, 10)
	}
	if account.URI != "" {
		return account.URI
	}
	return cfg.BaseURL() + "/users/" + url.PathEscape(account.Username)
}

func statusURL(cfg config.Config, status models.Status) string {
	value, _ := statusURLValue(cfg, status)
	return value
}

func statusURLValue(cfg config.Config, status models.Status) (string, bool) {
	if !statusLocal(status) {
		if status.URL.Valid {
			value := strings.TrimSpace(status.URL.String)
			if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
				return value, false
			}
		}
		return "", true
	}
	if status.URL.Valid && status.URL.String != "" {
		return status.URL.String, false
	}
	return cfg.BaseURL() + "/@" + url.PathEscape(status.Account.Username) + "/" + strconv.FormatInt(status.ID, 10), false
}

func statusLocal(status models.Status) bool {
	if status.Local.Valid {
		return status.Local.Bool
	}
	return status.Account.Local()
}

func statusURI(cfg config.Config, status models.Status) string {
	if statusLocal(status) && status.Account.ID > 0 && status.Account.IDScheme.Valid && status.Account.IDScheme.Int64 == 1 {
		return cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(status.Account.ID, 10) + "/statuses/" + strconv.FormatInt(status.ID, 10)
	}
	if status.URI.Valid && status.URI.String != "" {
		return status.URI.String
	}
	return cfg.BaseURL() + "/users/" + url.PathEscape(status.Account.Username) + "/statuses/" + strconv.FormatInt(status.ID, 10)
}

func accountAvatar(cfg config.Config, account models.Account, static bool) string {
	if account.AvatarRemoteURL.Valid && strings.TrimSpace(account.AvatarRemoteURL.String) != "" && cfg.DisableRemoteMediaCache {
		return account.AvatarRemoteURL.String
	}
	if account.SuspendedAt.Valid {
		return cfg.BaseURL() + "/avatars/original/missing.png"
	}
	if account.AvatarFileName.Valid && account.AvatarFileName.String != "" {
		style := "original"
		filename := account.AvatarFileName.String
		if static && strings.EqualFold(account.AvatarContentType.String, "image/gif") {
			style = "static"
			filename = paperclipStaticFilename(filename)
		}
		prefix := ""
		if account.AvatarUsesCachePrefix() {
			prefix = "cache/"
		}
		return cfg.SystemAssetURL(prefix + "accounts/avatars/" + paperclipIDPartition(account.ID) + "/" + style + "/" + url.PathEscape(filename))
	}
	return cfg.BaseURL() + "/avatars/original/missing.png"
}

func accountHeader(cfg config.Config, account models.Account, static bool) string {
	if strings.TrimSpace(account.HeaderRemoteURL) != "" && cfg.DisableRemoteMediaCache {
		return account.HeaderRemoteURL
	}
	if account.SuspendedAt.Valid {
		return cfg.BaseURL() + "/headers/original/missing.png"
	}
	if account.HeaderFileName.Valid && account.HeaderFileName.String != "" {
		style := "original"
		filename := account.HeaderFileName.String
		if static && strings.EqualFold(account.HeaderContentType.String, "image/gif") {
			style = "static"
			filename = paperclipStaticFilename(filename)
		}
		prefix := ""
		if account.HeaderUsesCachePrefix() {
			prefix = "cache/"
		}
		return cfg.SystemAssetURL(prefix + "accounts/headers/" + paperclipIDPartition(account.ID) + "/" + style + "/" + url.PathEscape(filename))
	}
	return cfg.BaseURL() + "/headers/original/missing.png"
}

func mediaAttachments(cfg config.Config, media []models.MediaAttachment) []MediaAttachment {
	items := make([]MediaAttachment, 0, len(media))
	for _, attachment := range media {
		kind := "unknown"
		switch attachment.Type {
		case 0:
			kind = "image"
		case 1:
			kind = "gifv"
		case 2:
			kind = "video"
		case 4:
			kind = "audio"
		case 3:
			kind = "unknown"
		}
		remote := ""
		if strings.TrimSpace(attachment.RemoteURL) != "" {
			remote = attachment.RemoteURL
		}
		fileURL := mediaAttachmentOriginalURL(cfg, attachment)
		preview := mediaAttachmentPreviewURL(cfg, attachment, fileURL)
		previewRemoteURL := ""
		if attachment.ThumbnailRemoteURL.Valid && strings.TrimSpace(attachment.ThumbnailRemoteURL.String) != "" {
			previewRemoteURL = attachment.ThumbnailRemoteURL.String
		}
		var meta any = map[string]any{}
		if len(attachment.FileMeta) > 0 {
			_ = json.Unmarshal(attachment.FileMeta, &meta)
		}
		items = append(items, MediaAttachment{
			ID:               strconv.FormatInt(attachment.ID, 10),
			Type:             kind,
			URL:              fileURL,
			PreviewURL:       preview,
			RemoteURL:        remote,
			PreviewRemoteURL: previewRemoteURL,
			TextURL:          mediaAttachmentTextURL(cfg, attachment),
			Meta:             meta,
			Description:      attachment.Description.String,
			Blurhash:         attachment.Blurhash.String,
		})
	}
	return items
}

func mediaAttachmentOriginalURL(cfg config.Config, attachment models.MediaAttachment) string {
	if mediaAttachmentDiscarded(attachment) {
		if attachment.Processing.Valid && attachment.Processing.Int64 != 2 {
			return ""
		}
		return mediaAttachmentProxyURL(cfg, attachment.ID, "original")
	}
	if strings.TrimSpace(attachment.RemoteURL) != "" && cfg.DisableRemoteMediaCache {
		return attachment.RemoteURL
	}
	if attachment.Processing.Valid && attachment.Processing.Int64 != 2 {
		return ""
	}
	if mediaAttachmentHasLocalFile(attachment) {
		return mediaAttachmentAssetURL(cfg, attachment, "files", "original", attachment.FileFileName.String)
	}
	if strings.TrimSpace(attachment.RemoteURL) != "" {
		return mediaAttachmentProxyURL(cfg, attachment.ID, "original")
	}
	return ""
}

func mediaAttachmentPreviewURL(cfg config.Config, attachment models.MediaAttachment, _ string) string {
	if mediaAttachmentDiscarded(attachment) {
		return mediaAttachmentProxyURL(cfg, attachment.ID, "small")
	}
	if strings.TrimSpace(attachment.RemoteURL) != "" && cfg.DisableRemoteMediaCache {
		return attachment.RemoteURL
	}
	if strings.TrimSpace(attachment.RemoteURL) != "" && !mediaAttachmentHasLocalFile(attachment) {
		return mediaAttachmentProxyURL(cfg, attachment.ID, "small")
	}
	if attachment.ThumbnailFileName.Valid && attachment.ThumbnailFileName.String != "" {
		return mediaAttachmentAssetURL(cfg, attachment, "thumbnails", "original", attachment.ThumbnailFileName.String)
	}
	if mediaAttachmentHasLocalFile(attachment) && mediaAttachmentProcessed(attachment) && mediaAttachmentHasSmallFileStyle(attachment) {
		return mediaAttachmentAssetURL(cfg, attachment, "files", "small", mediaAttachmentSmallStyleFilename(attachment))
	}
	return ""
}

func mediaAttachmentHasLocalFile(attachment models.MediaAttachment) bool {
	return attachment.FileFileName.Valid && strings.TrimSpace(attachment.FileFileName.String) != ""
}

func mediaAttachmentDiscarded(attachment models.MediaAttachment) bool {
	return attachment.Discarded || (attachment.Status.ID != 0 && attachment.Status.DeletedAt.Valid)
}

func markMediaAttachmentsDiscarded(attachments []models.MediaAttachment) []models.MediaAttachment {
	out := append([]models.MediaAttachment(nil), attachments...)
	for i := range out {
		out[i].Discarded = true
	}
	return out
}

func mediaAttachmentProcessed(attachment models.MediaAttachment) bool {
	return !attachment.Processing.Valid || attachment.Processing.Int64 == 2
}

func mediaAttachmentHasSmallFileStyle(attachment models.MediaAttachment) bool {
	return attachment.Type == 0 || attachment.Type == 1 || attachment.Type == 2
}

func mediaAttachmentSmallStyleFilename(attachment models.MediaAttachment) string {
	filename := strings.TrimSpace(attachment.FileFileName.String)
	if attachment.Type == 1 || attachment.Type == 2 {
		stem := strings.TrimSuffix(filename, filepath.Ext(filename))
		stem = strings.Trim(stem, "._")
		if stem == "" {
			stem = "thumbnail"
		}
		return stem + ".png"
	}
	return filename
}

func mediaAttachmentTextURL(cfg config.Config, attachment models.MediaAttachment) string {
	if strings.TrimSpace(attachment.RemoteURL) != "" || !attachment.Shortcode.Valid || strings.TrimSpace(attachment.Shortcode.String) == "" {
		return ""
	}
	return cfg.BaseURL() + "/media/" + url.PathEscape(attachment.Shortcode.String)
}

func mediaAttachmentProxyURL(cfg config.Config, id int64, version string) string {
	return cfg.BaseURL() + "/media_proxy/" + strconv.FormatInt(id, 10) + "/" + url.PathEscape(version)
}

func mediaAttachmentAssetURL(cfg config.Config, media models.MediaAttachment, attachment string, style string, filename string) string {
	prefix := ""
	if mediaAttachmentUsesCachePrefix(media) {
		prefix = "cache/"
	}
	return cfg.SystemAssetURL(prefix + "media_attachments/" + attachment + "/" + paperclipIDPartition(media.ID) + "/" + style + "/" + url.PathEscape(filename))
}

func mediaAttachmentUsesCachePrefix(media models.MediaAttachment) bool {
	return media.FileStorageSchemaVersion.Valid && media.FileStorageSchemaVersion.Int64 >= 1 && strings.TrimSpace(media.RemoteURL) != ""
}

func orderedStatusMediaAttachments(status models.Status) []models.MediaAttachment {
	var ordered []models.MediaAttachment
	if status.OrderedMediaAttachmentIDs == nil {
		ordered = mediaAttachmentsSortedByID(status.MediaAttachments)
	} else {
		byID := make(map[int64]models.MediaAttachment, len(status.MediaAttachments))
		for _, attachment := range status.MediaAttachments {
			byID[attachment.ID] = attachment
		}
		ordered = make([]models.MediaAttachment, 0, len(status.OrderedMediaAttachmentIDs))
		for _, id := range status.OrderedMediaAttachmentIDs {
			if attachment, ok := byID[id]; ok {
				ordered = append(ordered, attachment)
			}
		}
	}
	if status.DeletedAt.Valid {
		ordered = markMediaAttachmentsDiscarded(ordered)
	}
	return ordered
}

func mediaAttachmentsSortedByID(attachments []models.MediaAttachment) []models.MediaAttachment {
	if len(attachments) < 2 {
		return attachments
	}
	out := append([]models.MediaAttachment(nil), attachments...)
	sort.SliceStable(out, func(i int, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func previewCardFromStatus(cfg config.Config, status models.Status) any {
	card, ok := status.FirstPreviewCard()
	if !ok {
		return nil
	}
	return PreviewCardFromModel(cfg, card)
}

func previewCardType(value int) string {
	switch value {
	case 1:
		return "photo"
	case 2:
		return "video"
	case 3:
		return "rich"
	default:
		return "link"
	}
}

func previewCardImageURL(cfg config.Config, card models.PreviewCard) *string {
	if !card.ImageFileName.Valid || card.ImageFileName.String == "" {
		return nil
	}
	prefix := ""
	if card.ImageStorageSchemaVersion.Valid && card.ImageStorageSchemaVersion.Int64 >= 1 {
		prefix = "cache/"
	}
	value := cfg.SystemAssetURL(prefix + "preview_cards/images/" + paperclipIDPartition(card.ID) + "/original/" + url.PathEscape(card.ImageFileName.String))
	return &value
}

func customEmojiURL(cfg config.Config, emoji models.CustomEmoji, style string) string {
	if !emoji.ImageFileName.Valid || emoji.ImageFileName.String == "" {
		if emoji.ImageRemoteURL.Valid {
			return emoji.ImageRemoteURL.String
		}
		return ""
	}
	filename := emoji.ImageFileName.String
	if style == "static" {
		filename = paperclipStaticFilename(filename)
	}
	prefix := ""
	if !emoji.Local() && emoji.ImageStorageSchemaVersion.Valid && emoji.ImageStorageSchemaVersion.Int64 >= 1 {
		prefix = "cache/"
	}
	return cfg.SystemAssetURL(prefix + "custom_emojis/images/" + paperclipIDPartition(emoji.ID) + "/" + style + "/" + url.PathEscape(filename))
}

func paperclipIDPartition(id int64) string {
	value := strconv.FormatInt(id, 10)
	if len(value) < 9 {
		value = strings.Repeat("0", 9-len(value)) + value
	}
	parts := []string{}
	for len(value) > 3 {
		parts = append(parts, value[:3])
		value = value[3:]
	}
	parts = append(parts, value)
	return strings.Join(parts, "/")
}

func paperclipStaticFilename(filename string) string {
	base := filename
	if index := strings.LastIndex(base, "."); index > 0 {
		base = base[:index]
	}
	return base + ".png"
}

func mentions(cfg config.Config, mentions []models.Mention) []Mention {
	ordered := append([]models.Mention(nil), mentions...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})
	items := make([]Mention, 0, len(ordered))
	for _, mention := range ordered {
		if !mention.AccountID.Valid {
			continue
		}
		items = append(items, Mention{
			ID:       strconv.FormatInt(mention.AccountID.Int64, 10),
			Username: mention.Account.Username,
			URL:      accountURL(cfg, mention.Account),
			Acct:     mention.Account.Acct(),
		})
	}
	return items
}

func tags(cfg config.Config, tags []models.Tag) []Tag {
	items := make([]Tag, 0, len(tags))
	for _, tag := range tags {
		items = append(items, Tag{Name: tag.Name, URL: cfg.BaseURL() + "/tags/" + url.PathEscape(tag.Name)})
	}
	return items
}

func announcementStatuses(cfg config.Config, statuses []models.Status) []AnnouncementStatus {
	items := make([]AnnouncementStatus, 0, len(statuses))
	for _, status := range statuses {
		statusURL, statusURLNull := statusURLValue(cfg, status)
		items = append(items, AnnouncementStatus{
			ID:      strconv.FormatInt(status.ID, 10),
			URL:     statusURL,
			URLNull: statusURLNull,
		})
	}
	return items
}

func announcementContentHTML(cfg config.Config, announcement models.Announcement) string {
	paragraphs := strings.Split(strings.ReplaceAll(strings.ReplaceAll(announcement.Text, "\r\n", "\n"), "\r", "\n"), "\n\n")
	var out strings.Builder
	mentions := statusMentionResolver{accounts: announcement.MentionAccounts}
	for _, paragraph := range paragraphs {
		paragraph = strings.Trim(paragraph, "\n")
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		out.WriteString("<p>")
		out.WriteString(strings.ReplaceAll(statusLinkifyInline(cfg, paragraph, mentions), "\n", "<br>"))
		out.WriteString("</p>\n")
	}
	return out.String()
}

func mastodonLinkTokenBoundaryOK(text string, index int, token string) bool {
	if strings.HasPrefix(token, "#") || strings.HasPrefix(token, "＃") {
		return mastodonHashtagBoundaryOK(text, index)
	}
	if strings.HasPrefix(token, "@") {
		return mastodonMentionBoundaryOK(text, index)
	}
	return announcementLinkBoundaryOK(text, index)
}

func mastodonHashtagBoundaryOK(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index+1])
	return !(r == '=' || r == '/' || r == ')' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
}

func mastodonMentionBoundaryOK(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index+1])
	return !(r == '=' || r == '/' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
}

func announcementLinkBoundaryOK(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	ch := text[index]
	return !(ch == '_' || (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z'))
}

func trimTrailingLinkPunctuation(raw string) (string, string) {
	token := raw
	trailing := ""
	for token != "" {
		last := token[len(token)-1]
		if last == ')' && strings.Count(token, ")") <= strings.Count(token, "(") {
			break
		}
		if !strings.ContainsRune(".,!?:;)]}>\"'「」", rune(last)) {
			break
		}
		token = token[:len(token)-1]
		trailing = string(last) + trailing
	}
	return token, trailing
}

func announcementAccounts(cfg config.Config, accounts []models.Account) []AnnouncementAccount {
	items := make([]AnnouncementAccount, 0, len(accounts))
	for _, account := range accounts {
		items = append(items, AnnouncementAccount{
			ID:       strconv.FormatInt(account.ID, 10),
			Username: account.Username,
			URL:      accountURL(cfg, account),
			Acct:     account.Acct(),
		})
	}
	return items
}

func announcementTags(cfg config.Config, text string) []Tag {
	matches := hashtagPattern.FindAllStringSubmatchIndex(text, -1)
	items := []Tag{}
	seen := map[string]struct{}{}
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		if !mastodonHashtagBoundaryOK(text, match[0]-1) {
			continue
		}
		name := strings.TrimSpace(text[match[2]:match[3]])
		if !mastodonHashtagNameValid(name) {
			continue
		}
		normalized := strings.ToLower(name)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		items = append(items, Tag{Name: name, URL: cfg.BaseURL() + "/tags/" + url.PathEscape(normalized)})
	}
	return items
}

func mastodonHashtagNameValid(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || r == '·' || r == '・' || r == '\u200c' {
			return true
		}
	}
	return false
}

func reactionsFromSource(reactions []ReactionSource, includeMe bool) []Reaction {
	items := make([]Reaction, 0, len(reactions))
	for _, reaction := range reactions {
		var me *bool
		if includeMe {
			value := reaction.Me
			me = &value
		}
		item := Reaction{Name: reaction.Name, Count: reaction.Count, Me: me}
		if reaction.URL != "" {
			item.URL = &reaction.URL
		}
		if reaction.StaticURL != "" {
			item.StaticURL = &reaction.StaticURL
		}
		items = append(items, item)
	}
	return items
}

type webPushDataPayload struct {
	Policy string         `json:"policy"`
	Alerts map[string]any `json:"alerts"`
}

func webPushData(raw models.JSONValue) webPushDataPayload {
	out := webPushDataPayload{Policy: "all", Alerts: map[string]any{}}
	if len(raw) == 0 {
		return out
	}
	var decoded struct {
		Policy string         `json:"policy"`
		Alerts map[string]any `json:"alerts"`
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return out
	}
	out.Policy = firstNonEmptyString(decoded.Policy, "all")
	for key, value := range decoded.Alerts {
		out.Alerts[key] = railsBooleanCast(value)
	}
	return out
}

func railsBooleanCast(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case bool:
		return typed
	case string:
		if typed == "" {
			return nil
		}
		return !railsFalseString(typed)
	case float64:
		return typed != 0
	default:
		return true
	}
}

func railsFalseString(value string) bool {
	switch value {
	case "false", "FALSE", "0", "f", "F", "off", "OFF":
		return true
	default:
		return false
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func featuredTagURL(cfg config.Config, account models.Account, tag models.Tag) string {
	acct := url.PathEscape(account.Username)
	if !account.Local() {
		acct += "@" + url.PathEscape(account.Domain.String)
	}
	return cfg.BaseURL() + "/@" + acct + "/tagged/" + url.PathEscape(tag.Name)
}

func domainBlockSeverity(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	switch value.Int64 {
	case 1:
		return "suspend"
	case 2:
		return "noop"
	default:
		return "silence"
	}
}

func ipBlockSeverity(value int) string {
	switch value {
	case 5000:
		return "sign_up_requires_approval"
	case 5500:
		return "sign_up_block"
	case 9999:
		return "no_access"
	default:
		return ""
	}
}

func obfuscateDomain(domain string, obfuscate bool) string {
	if !obfuscate {
		return domain
	}
	runes := []rune(domain)
	visibleRatio := len(runes) / 4
	for index, char := range runes {
		if index > visibleRatio && index < len(runes)-visibleRatio && char != '.' {
			runes[index] = '*'
		}
	}
	return string(runes)
}

func simpleMarkdownHTML(markdown string) string {
	return simpleMarkdownHTMLWithOptions(markdown, simpleMarkdownOptions{EscapeHTML: true})
}

// MarkdownHTML renders the same escaped, image-free Markdown subset used by
// Mastodon's server-authored policy and description surfaces.
func MarkdownHTML(markdown string) string {
	return simpleMarkdownHTML(markdown)
}

type simpleMarkdownOptions struct {
	EscapeHTML bool
}

func simpleMarkdownHTMLWithOptions(markdown string, options simpleMarkdownOptions) string {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var out strings.Builder
	var paragraph []string
	listType := ""
	inBlockquote := false
	inCodeFence := false
	var codeFence []string

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		out.WriteString("<p>")
		out.WriteString(markdownInlineHTMLWithOptions(strings.Join(paragraph, " "), options))
		out.WriteString("</p>\n")
		paragraph = nil
	}
	closeList := func() {
		if listType == "" {
			return
		}
		out.WriteString("</" + listType + ">\n")
		listType = ""
	}
	closeBlockquote := func() {
		if !inBlockquote {
			return
		}
		out.WriteString("</blockquote>\n")
		inBlockquote = false
	}
	openList := func(kind string) {
		if listType == kind {
			return
		}
		closeList()
		out.WriteString("<" + kind + ">\n")
		listType = kind
	}
	flushCodeFence := func() {
		if !inCodeFence {
			return
		}
		out.WriteString("<pre><code>")
		out.WriteString(html.EscapeString(strings.Join(codeFence, "\n")))
		out.WriteString("</code></pre>\n")
		codeFence = nil
		inCodeFence = false
	}

	for _, rawLine := range lines {
		if strings.HasPrefix(strings.TrimSpace(rawLine), "```") {
			if inCodeFence {
				flushCodeFence()
			} else {
				flushParagraph()
				closeList()
				closeBlockquote()
				inCodeFence = true
			}
			continue
		}
		if inCodeFence {
			codeFence = append(codeFence, rawLine)
			continue
		}

		line := strings.TrimSpace(rawLine)
		if line == "" {
			flushParagraph()
			closeList()
			closeBlockquote()
			continue
		}
		switch {
		case line == "---" || line == "***" || line == "___":
			flushParagraph()
			closeList()
			closeBlockquote()
			out.WriteString("<hr />\n")
		case strings.HasPrefix(line, "### "):
			flushParagraph()
			closeList()
			closeBlockquote()
			out.WriteString("<h3>" + markdownInlineHTMLWithOptions(strings.TrimSpace(strings.TrimPrefix(line, "### ")), options) + "</h3>\n")
		case strings.HasPrefix(line, "## "):
			flushParagraph()
			closeList()
			closeBlockquote()
			out.WriteString("<h2>" + markdownInlineHTMLWithOptions(strings.TrimSpace(strings.TrimPrefix(line, "## ")), options) + "</h2>\n")
		case strings.HasPrefix(line, "# "):
			flushParagraph()
			closeList()
			closeBlockquote()
			out.WriteString("<h1>" + markdownInlineHTMLWithOptions(strings.TrimSpace(strings.TrimPrefix(line, "# ")), options) + "</h1>\n")
		case strings.HasPrefix(line, "- "):
			flushParagraph()
			closeBlockquote()
			openList("ul")
			out.WriteString("<li>" + markdownInlineHTMLWithOptions(strings.TrimSpace(strings.TrimPrefix(line, "- ")), options) + "</li>\n")
		case orderedMarkdownListItem(line):
			flushParagraph()
			closeBlockquote()
			openList("ol")
			item := strings.TrimSpace(line[strings.Index(line, ".")+1:])
			out.WriteString("<li>" + markdownInlineHTMLWithOptions(item, options) + "</li>\n")
		case strings.HasPrefix(line, "> "):
			flushParagraph()
			closeList()
			if !inBlockquote {
				out.WriteString("<blockquote>\n")
				inBlockquote = true
			}
			out.WriteString("<p>" + markdownInlineHTMLWithOptions(strings.TrimSpace(strings.TrimPrefix(line, "> ")), options) + "</p>\n")
		default:
			closeList()
			closeBlockquote()
			paragraph = append(paragraph, line)
		}
	}
	flushCodeFence()
	flushParagraph()
	closeList()
	closeBlockquote()
	return out.String()
}

func orderedMarkdownListItem(line string) bool {
	dot := strings.Index(line, ".")
	if dot <= 0 || dot+1 >= len(line) || line[dot+1] != ' ' {
		return false
	}
	for _, char := range line[:dot] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func markdownInlineHTML(value string) string {
	return markdownInlineHTMLWithOptions(value, simpleMarkdownOptions{EscapeHTML: true})
}

func markdownInlineHTMLWithOptions(value string, options simpleMarkdownOptions) string {
	codeSpans := []string{}
	value = regexp.MustCompile("`([^`]+)`").ReplaceAllStringFunc(value, func(match string) string {
		code := strings.TrimSuffix(strings.TrimPrefix(match, "`"), "`")
		index := len(codeSpans)
		codeSpans = append(codeSpans, "<code>"+html.EscapeString(code)+"</code>")
		return "\x00CODE" + strconv.Itoa(index) + "\x00"
	})

	rendered := value
	if options.EscapeHTML {
		rendered = html.EscapeString(rendered)
	}
	rendered = regexp.MustCompile(`!?\[([^\]]+)\]\((https?://[^)\s]+|mailto:[^)\s]+|/[^)\s]*)\)`).ReplaceAllStringFunc(rendered, func(match string) string {
		parts := regexp.MustCompile(`^!?\[([^\]]+)\]\(([^)]+)\)$`).FindStringSubmatch(match)
		if len(parts) != 3 || strings.HasPrefix(match, "![") {
			return match
		}
		href := html.UnescapeString(parts[2])
		if !safeMarkdownLinkURL(href) {
			return parts[1]
		}
		return `<a href="` + html.EscapeString(href) + `" rel="nofollow noopener noreferrer" target="_blank">` + parts[1] + `</a>`
	})
	rendered = regexp.MustCompile(`\*\*([^*]+)\*\*`).ReplaceAllString(rendered, `<strong>$1</strong>`)
	rendered = regexp.MustCompile(`__([^_]+)__`).ReplaceAllString(rendered, `<strong>$1</strong>`)
	rendered = regexp.MustCompile(`\*([^*\s][^*]*[^*\s]|\S)\*`).ReplaceAllString(rendered, `<em>$1</em>`)
	rendered = regexp.MustCompile(`_([^_\s][^_]*[^_\s]|\S)_`).ReplaceAllString(rendered, `<em>$1</em>`)

	for index, code := range codeSpans {
		rendered = strings.ReplaceAll(rendered, "\x00CODE"+strconv.Itoa(index)+"\x00", code)
	}
	return rendered
}

func safeMarkdownLinkURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.IsAbs() {
		scheme := strings.ToLower(parsed.Scheme)
		return scheme == "http" || scheme == "https" || scheme == "mailto"
	}
	return strings.HasPrefix(raw, "/")
}

const defaultPrivacyPolicy = `This privacy policy describes how %{domain} collects, protects and uses information you provide through the %{domain} website and API.

# What information do we collect?

- Basic account information, such as username, e-mail address, password hash, profile text, avatar, and header.
- Posts, follows, favourites, boosts, bookmarks, and other information required to operate a federated social network.
- IP addresses and request metadata needed for security, abuse prevention, and troubleshooting.

# How do we use your information?

We use this information to provide Mastodon-compatible social networking features, deliver federation messages, moderate the service, and maintain account security.

# Data retention

You may delete posts and your account through the service. Server logs and safety records may be retained for operational, security, or legal reasons.

# Cookies

The service uses cookies and access tokens to keep you signed in and to remember preferences.

# Federation

Public and followers-only content may be delivered to other servers in the fediverse according to the visibility of each post.`

func fieldsFromJSON(cfg config.Config, account models.Account) []Field {
	raw := account.Fields
	if len(raw) == 0 {
		return []Field{}
	}
	var decoded []struct {
		Name       string  `json:"name"`
		Value      string  `json:"value"`
		VerifiedAt *string `json:"verified_at"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return []Field{}
	}
	fields := make([]Field, 0, len(decoded))
	for _, field := range decoded {
		name := sanitizeAccountFieldText(account.Local(), field.Name)
		if name == "" {
			continue
		}
		item := Field{Name: name, Value: sanitizeAccountFieldText(account.Local(), field.Value), VerifiedAt: field.VerifiedAt}
		item.Value = accountFieldValueHTML(cfg, account, item)
		fields = append(fields, item)
	}
	return fields
}

func sourceFieldsFromJSON(raw []byte) []SourceField {
	if len(raw) == 0 {
		return []SourceField{}
	}
	var decoded []struct {
		Name       string  `json:"name"`
		Value      string  `json:"value"`
		VerifiedAt *string `json:"verified_at"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return []SourceField{}
	}
	fields := make([]SourceField, 0, len(decoded))
	for _, field := range decoded {
		name := sanitizeLocalAccountFieldText(field.Name)
		if name == "" {
			continue
		}
		fields = append(fields, SourceField{Name: name, Value: sanitizeLocalAccountFieldText(field.Value), VerifiedAt: field.VerifiedAt})
	}
	return fields
}

func sanitizeLocalAccountFieldText(value string) string {
	return sanitizeAccountFieldText(true, value)
}

func sanitizeAccountFieldText(local bool, value string) string {
	value = strings.TrimSpace(value)
	limit := 2047
	if local {
		limit = 255
	}
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func userSettings(user models.User) map[string]any {
	settings := map[string]any{}
	if user.Settings.Valid && strings.TrimSpace(user.Settings.String) != "" {
		_ = json.Unmarshal([]byte(user.Settings.String), &settings)
	}
	return settings
}

func stringSetting(settings map[string]any, key string, fallback string) string {
	if value, ok := settings[key].(string); ok {
		return value
	}
	return fallback
}

func stringSettingPtr(settings map[string]any, key string) *string {
	if value, ok := settings[key].(string); ok {
		return &value
	}
	return nil
}

func boolSetting(settings map[string]any, key string, fallback bool) bool {
	switch value := settings[key].(type) {
	case bool:
		return value
	case string:
		return value == "true" || value == "1"
	default:
		return fallback
	}
}

func visibilityName(value int) string {
	switch value {
	case 1:
		return "unlisted"
	case 2, 4:
		return "private"
	case 3:
		return "direct"
	default:
		return "public"
	}
}

func filterActionName(value int) string {
	switch value {
	case 1:
		return "hide"
	case 2:
		return "blur"
	default:
		return "warn"
	}
}

func reportCategoryName(value int) string {
	switch value {
	case 1000:
		return "spam"
	case 1500:
		return "legal"
	case 2000:
		return "violation"
	default:
		return "other"
	}
}

func int64Strings(values []int64) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.FormatInt(value, 10))
	}
	return out
}

func idPtr(value sql.NullInt64) *string {
	if !value.Valid {
		return nil
	}
	id := strconv.FormatInt(value.Int64, 10)
	return &id
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func boolPtr(value sql.NullBool) *bool {
	if !value.Valid {
		return nil
	}
	return &value.Bool
}

func timePtr(value sql.NullTime) *string {
	if !value.Valid {
		return nil
	}
	formatted := restTimestamp(value.Time)
	return &formatted
}

func dateString(value sql.NullTime) *string {
	if !value.Valid {
		return nil
	}
	formatted := value.Time.UTC().Format("2006-01-02")
	return &formatted
}

func boolPtrIf(condition bool, value bool) *bool {
	if !condition {
		return nil
	}
	return &value
}

func emptyIf(condition bool, value string) string {
	if condition {
		return ""
	}
	return value
}
