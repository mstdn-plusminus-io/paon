package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAuthRoutesResolveAcceptLanguageAndKeepMastodonShell(t *testing.T) {
	s, err := NewServer(config.Config{
		Title:       "Paon",
		LocalDomain: "example.com",
		WebDomain:   "example.com",
		Scheme:      "https",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		path           string
		acceptLanguage string
		want           []string
		forbid         []string
	}{
		{
			name:           "sign in ja",
			path:           "/auth/sign_in",
			acceptLanguage: "ja",
			want: []string{
				`<html lang="ja">`,
				`class="container-alt"`,
				`class="logo-container"`,
				`class="form-container"`,
				`src="/packs/`,
				`example.comにログイン`,
				`メールアドレス`,
				`パスワードをお忘れですか？`,
				`登録する`,
			},
			forbid: []string{
				`Login to example.com`,
				`>Email address<`,
				`>Forgot your password?<`,
				`>Sign up<`,
			},
		},
		{
			name:           "sign in de",
			path:           "/auth/sign_in",
			acceptLanguage: "de",
			want: []string{
				`<html lang="de">`,
				`class="container-alt"`,
				`class="logo-container"`,
				`class="form-container"`,
				`src="/packs/`,
				`Bei example.com anmelden`,
				`E-Mail-Adresse`,
				`Passwort vergessen?`,
				`Registrieren`,
			},
			forbid: []string{
				`Login to example.com`,
				`>Email address<`,
				`>Forgot your password?<`,
				`>Sign up<`,
			},
		},
		{
			name:           "password reset ja",
			path:           "/auth/password/new",
			acceptLanguage: "ja",
			want: []string{
				`<html lang="ja">`,
				`class="container-alt"`,
				`class="logo-container"`,
				`class="form-container"`,
				`src="/packs/`,
				`パスワードを再発行`,
				`メールアドレス`,
				`ログイン`,
				`登録する`,
			},
			forbid: []string{
				`>Reset password<`,
				`>Email address<`,
				`>Log in<`,
				`>Sign up<`,
			},
		},
		{
			name:           "password reset de",
			path:           "/auth/password/new",
			acceptLanguage: "de",
			want: []string{
				`<html lang="de">`,
				`class="container-alt"`,
				`class="logo-container"`,
				`class="form-container"`,
				`src="/packs/`,
				`Passwort zurücksetzen`,
				`E-Mail-Adresse`,
				`Anmelden`,
				`Registrieren`,
			},
			forbid: []string{
				`>Reset password<`,
				`>Email address<`,
				`>Log in<`,
				`>Sign up<`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Accept-Language", tt.acceptLanguage)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", rec.Code, body)
			}
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Fatalf("%s missing %q\nbody=%s", tt.path, want, body)
				}
			}
			for _, forbidden := range tt.forbid {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s unexpectedly contained English literal %q\nbody=%s", tt.path, forbidden, body)
				}
			}
		})
	}
}

func TestRegistrationHTMLResolvesLocalizedAuthCopy(t *testing.T) {
	for _, tt := range []struct {
		name   string
		locale string
		want   []string
		forbid []string
	}{
		{
			name:   "ja",
			locale: "ja",
			want: []string{
				`<html lang="ja">`,
				`登録する`,
				`ユーザー情報`,
				`さあ example.com でセットアップしましょう.`,
				`メールアドレス`,
				`パスワード（確認用）`,
				`ログイン`,
				`確認メールを受信できない場合は`,
			},
			forbid: []string{
				`>Sign up<`,
				`Your details`,
				`Let&#39;s get you set up on example.com.`,
				`>Email address<`,
				`>Confirm password<`,
				`>Log in<`,
			},
		},
		{
			name:   "de",
			locale: "de",
			want: []string{
				`<html lang="de">`,
				`Registrieren`,
				`Deine Daten`,
				`Lass uns dein Konto auf example.com einrichten.`,
				`E-Mail-Adresse`,
				`Passwort bestätigen`,
				`Anmelden`,
				`Keinen Bestätigungslink erhalten?`,
			},
			forbid: []string{
				`>Sign up<`,
				`Your details`,
				`Let&#39;s get you set up on example.com.`,
				`>Email address<`,
				`>Confirm password<`,
				`>Log in<`,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := registrationHTMLWithTurnstile("example.com", "", "", false, "", tt.locale, true, "accept-token")
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Fatalf("registration html missing %q\nbody=%s", want, body)
				}
			}
			for _, forbidden := range tt.forbid {
				if strings.Contains(body, forbidden) {
					t.Fatalf("registration html unexpectedly contained English literal %q\nbody=%s", forbidden, body)
				}
			}
		})
	}
}
