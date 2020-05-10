package sessions_test

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/oauth2-proxy/oauth2-proxy/pkg/apis/options"
	sessionsapi "github.com/oauth2-proxy/oauth2-proxy/pkg/apis/sessions"
	cookiesapi "github.com/oauth2-proxy/oauth2-proxy/pkg/cookies"
	"github.com/oauth2-proxy/oauth2-proxy/pkg/encryption"
	"github.com/oauth2-proxy/oauth2-proxy/pkg/logger"
	"github.com/oauth2-proxy/oauth2-proxy/pkg/sessions"
	sessionscookie "github.com/oauth2-proxy/oauth2-proxy/pkg/sessions/cookie"
	"github.com/oauth2-proxy/oauth2-proxy/pkg/sessions/redis"
	"github.com/oauth2-proxy/oauth2-proxy/pkg/sessions/utils"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestSessionStore(t *testing.T) {
	logger.SetOutput(GinkgoWriter)

	RegisterFailHandler(Fail)
	RunSpecs(t, "SessionStore")
}

var _ = Describe("NewSessionStore", func() {
	var opts *options.SessionOptions
	var cookieOpts *options.CookieOptions

	var request *http.Request
	var response *httptest.ResponseRecorder
	var session *sessionsapi.SessionState
	var ss sessionsapi.SessionStore
	var mr *miniredis.Miniredis

	CheckCookieOptions := func() {
		Context("the cookies returned", func() {
			var cookies []*http.Cookie
			BeforeEach(func() {
				cookies = response.Result().Cookies()
			})

			It("have the correct name set", func() {
				if len(cookies) == 1 {
					Expect(cookies[0].Name).To(Equal(cookieOpts.Name))
				} else {
					for _, cookie := range cookies {
						Expect(cookie.Name).To(ContainSubstring(cookieOpts.Name))
					}
				}
			})

			It("have the correct path set", func() {
				for _, cookie := range cookies {
					Expect(cookie.Path).To(Equal(cookieOpts.Path))
				}
			})

			It("have the correct domain set", func() {
				for _, cookie := range cookies {
					specifiedDomain := ""
					if len(cookieOpts.Domains) > 0 {
						specifiedDomain = cookieOpts.Domains[0]
					}
					Expect(cookie.Domain).To(Equal(specifiedDomain))
				}
			})

			It("have the correct HTTPOnly set", func() {
				for _, cookie := range cookies {
					Expect(cookie.HttpOnly).To(Equal(cookieOpts.HTTPOnly))
				}
			})

			It("have the correct secure set", func() {
				for _, cookie := range cookies {
					Expect(cookie.Secure).To(Equal(cookieOpts.Secure))
				}
			})

			It("have the correct SameSite set", func() {
				for _, cookie := range cookies {
					Expect(cookie.SameSite).To(Equal(cookiesapi.ParseSameSite(cookieOpts.SameSite)))
				}
			})

			It("have a signature timestamp matching session.CreatedAt", func() {
				for _, cookie := range cookies {
					if cookie.Value != "" {
						parts := strings.Split(cookie.Value, "|")
						Expect(parts).To(HaveLen(3))
						Expect(parts[1]).To(Equal(strconv.Itoa(int(session.CreatedAt.Unix()))))
					}
				}
			})

		})
	}

	// The following should only be for server stores
	PersistentSessionStoreTests := func() {
		Context("when Clear is called on a persistent store", func() {
			var resultCookies []*http.Cookie

			BeforeEach(func() {
				req := httptest.NewRequest("GET", "http://example.com/", nil)
				saveResp := httptest.NewRecorder()
				err := ss.Save(saveResp, req, session)
				Expect(err).ToNot(HaveOccurred())

				resultCookies = saveResp.Result().Cookies()
				for _, c := range resultCookies {
					request.AddCookie(c)
				}
				err = ss.Clear(response, request)
				Expect(err).ToNot(HaveOccurred())
			})

			Context("attempting to Load", func() {
				var loadedAfterClear *sessionsapi.SessionState
				var loadErr error

				BeforeEach(func() {
					loadReq := httptest.NewRequest("GET", "http://example.com/", nil)
					for _, c := range resultCookies {
						loadReq.AddCookie(c)
					}

					loadedAfterClear, loadErr = ss.Load(loadReq)
				})

				It("returns an empty session", func() {
					Expect(loadedAfterClear).To(BeNil())
				})

				It("returns an error", func() {
					Expect(loadErr).To(HaveOccurred())
				})
			})

			CheckCookieOptions()
		})
	}

	SessionStoreInterfaceTests := func(persistent bool) {
		Context("when Save is called", func() {
			Context("with no existing session", func() {
				BeforeEach(func() {
					err := ss.Save(response, request, session)
					Expect(err).ToNot(HaveOccurred())
				})

				It("sets a `set-cookie` header in the response", func() {
					Expect(response.Header().Get("set-cookie")).ToNot(BeEmpty())
				})

				It("Ensures the session CreatedAt is not zero", func() {
					Expect(session.CreatedAt.IsZero()).To(BeFalse())
				})
			})

			Context("with a broken session", func() {
				BeforeEach(func() {
					By("Using a valid cookie with a different providers session encoding")
					broken := "BrokenSessionFromADifferentSessionImplementation"
					value := encryption.SignedValue(cookieOpts.Secret, cookieOpts.Name, broken, time.Now())
					cookie := cookiesapi.MakeCookieFromOptions(request, cookieOpts.Name, value, cookieOpts, cookieOpts.Expire, time.Now())
					request.AddCookie(cookie)

					err := ss.Save(response, request, session)
					Expect(err).ToNot(HaveOccurred())
				})

				It("sets a `set-cookie` header in the response", func() {
					Expect(response.Header().Get("set-cookie")).ToNot(BeEmpty())
				})

				It("Ensures the session CreatedAt is not zero", func() {
					Expect(session.CreatedAt.IsZero()).To(BeFalse())
				})
			})

			Context("with an expired saved session", func() {
				var err error
				BeforeEach(func() {
					By("saving a session")
					req := httptest.NewRequest("GET", "http://example.com/", nil)
					saveResp := httptest.NewRecorder()
					err = ss.Save(saveResp, req, session)
					Expect(err).ToNot(HaveOccurred())

					By("and clearing the session")
					for _, c := range saveResp.Result().Cookies() {
						request.AddCookie(c)
					}
					clearResp := httptest.NewRecorder()
					err = ss.Clear(clearResp, request)
					Expect(err).ToNot(HaveOccurred())

					By("then saving a request with the cleared session")
					err = ss.Save(response, request, session)
				})

				It("no error should occur", func() {
					Expect(err).ToNot(HaveOccurred())
				})
			})

			CheckCookieOptions()
		})

		Context("when Clear is called", func() {
			BeforeEach(func() {
				req := httptest.NewRequest("GET", "http://example.com/", nil)
				saveResp := httptest.NewRecorder()
				err := ss.Save(saveResp, req, session)
				Expect(err).ToNot(HaveOccurred())

				for _, c := range saveResp.Result().Cookies() {
					request.AddCookie(c)
				}
				err = ss.Clear(response, request)
				Expect(err).ToNot(HaveOccurred())
			})

			It("sets a `set-cookie` header in the response", func() {
				Expect(response.Header().Get("Set-Cookie")).ToNot(BeEmpty())
			})

			CheckCookieOptions()
		})

		Context("when Load is called", func() {
			LoadSessionTests := func() {
				var loadedSession *sessionsapi.SessionState
				BeforeEach(func() {
					var err error
					loadedSession, err = ss.Load(request)
					Expect(err).ToNot(HaveOccurred())
				})

				It("loads a session equal to the original session", func() {
					if cookieOpts.Secret == "" {
						// Only Email and User stored in session when encrypted
						Expect(loadedSession.Email).To(Equal(session.Email))
						Expect(loadedSession.User).To(Equal(session.User))
					} else {
						// All fields stored in session if encrypted

						// Can't compare time.Time using Equal() so remove ExpiresOn from sessions
						l := *loadedSession
						l.CreatedAt = nil
						l.ExpiresOn = nil
						s := *session
						s.CreatedAt = nil
						s.ExpiresOn = nil
						Expect(l).To(Equal(s))

						// Compare time.Time separately
						Expect(loadedSession.CreatedAt.Equal(*session.CreatedAt)).To(BeTrue())
						Expect(loadedSession.ExpiresOn.Equal(*session.ExpiresOn)).To(BeTrue())
					}
				})
			}

			BeforeEach(func() {
				req := httptest.NewRequest("GET", "http://example.com/", nil)
				resp := httptest.NewRecorder()
				err := ss.Save(resp, req, session)
				Expect(err).ToNot(HaveOccurred())

				for _, cookie := range resp.Result().Cookies() {
					request.AddCookie(cookie)
				}
			})

			Context("before the refresh period", func() {
				LoadSessionTests()
			})

			// Test TTLs and cleanup of persistent session storage
			// For non-persistent we rely on the browser cookie lifecycle
			if persistent {
				Context("after the refresh period, but before the cookie expire period", func() {
					BeforeEach(func() {
						switch ss.(type) {
						case *redis.SessionStore:
							mr.FastForward(cookieOpts.Refresh + time.Minute)
						}
					})

					LoadSessionTests()
				})

				Context("after the cookie expire period", func() {
					var loadedSession *sessionsapi.SessionState
					var err error

					BeforeEach(func() {
						switch ss.(type) {
						case *redis.SessionStore:
							mr.FastForward(cookieOpts.Expire + time.Minute)
						}

						loadedSession, err = ss.Load(request)
						Expect(err).To(HaveOccurred())
					})

					It("returns an error loading the session", func() {
						Expect(err).To(HaveOccurred())
					})

					It("returns an empty session", func() {
						Expect(loadedSession).To(BeNil())
					})
				})
			}
		})

		if persistent {
			PersistentSessionStoreTests()
		}
	}

	RunSessionTests := func(persistent bool) {
		Context("with default options", func() {
			BeforeEach(func() {
				var err error
				ss, err = sessions.NewSessionStore(opts, cookieOpts)
				Expect(err).ToNot(HaveOccurred())
			})

			SessionStoreInterfaceTests(persistent)
		})

		Context("with non-default options", func() {
			BeforeEach(func() {
				cookieOpts = &options.CookieOptions{
					Name:     "_cookie_name",
					Path:     "/path",
					Expire:   time.Duration(72) * time.Hour,
					Refresh:  time.Duration(2) * time.Hour,
					Secure:   false,
					HTTPOnly: false,
					Domains:  []string{"example.com"},
					SameSite: "strict",
				}

				var err error
				ss, err = sessions.NewSessionStore(opts, cookieOpts)
				Expect(err).ToNot(HaveOccurred())
			})

			SessionStoreInterfaceTests(persistent)
		})

		Context("with a cipher", func() {
			BeforeEach(func() {
				secret := make([]byte, 32)
				_, err := rand.Read(secret)
				Expect(err).ToNot(HaveOccurred())
				cookieOpts.Secret = base64.URLEncoding.EncodeToString(secret)
				cipher, err := encryption.NewCipher(utils.SecretBytes(cookieOpts.Secret))
				Expect(err).ToNot(HaveOccurred())
				Expect(cipher).ToNot(BeNil())
				opts.Cipher = cipher

				ss, err = sessions.NewSessionStore(opts, cookieOpts)
				Expect(err).ToNot(HaveOccurred())
			})

			SessionStoreInterfaceTests(persistent)
		})
	}

	BeforeEach(func() {
		ss = nil
		opts = &options.SessionOptions{}

		// Set default options in CookieOptions
		cookieOpts = &options.CookieOptions{
			Name:     "_oauth2_proxy",
			Path:     "/",
			Expire:   time.Duration(168) * time.Hour,
			Refresh:  time.Duration(1) * time.Hour,
			Secure:   true,
			HTTPOnly: true,
			SameSite: "",
		}

		expires := time.Now().Add(1 * time.Hour)
		session = &sessionsapi.SessionState{
			AccessToken:  "AccessToken",
			IDToken:      "IDToken",
			ExpiresOn:    &expires,
			RefreshToken: "RefreshToken",
			Email:        "john.doe@example.com",
			User:         "john.doe",
		}

		request = httptest.NewRequest("GET", "http://example.com/", nil)
		response = httptest.NewRecorder()
	})

	Context("with type 'cookie'", func() {
		BeforeEach(func() {
			opts.Type = options.CookieSessionStoreType
		})

		It("creates a cookie.SessionStore", func() {
			ss, err := sessions.NewSessionStore(opts, cookieOpts)
			Expect(err).NotTo(HaveOccurred())
			Expect(ss).To(BeAssignableToTypeOf(&sessionscookie.SessionStore{}))
		})

		Context("the cookie.SessionStore", func() {
			RunSessionTests(false)
		})
	})

	Context("with type 'redis'", func() {
		BeforeEach(func() {
			var err error
			mr, err = miniredis.Run()
			Expect(err).ToNot(HaveOccurred())
			opts.Type = options.RedisSessionStoreType
			opts.Redis.ConnectionURL = "redis://" + mr.Addr()
		})

		AfterEach(func() {
			mr.Close()
		})

		It("creates a redis.SessionStore", func() {
			ss, err := sessions.NewSessionStore(opts, cookieOpts)
			Expect(err).NotTo(HaveOccurred())
			Expect(ss).To(BeAssignableToTypeOf(&redis.SessionStore{}))
		})

		Context("the redis.SessionStore", func() {
			RunSessionTests(true)
		})
	})

	Context("with an invalid type", func() {
		BeforeEach(func() {
			opts.Type = "invalid-type"
		})

		It("returns an error", func() {
			ss, err := sessions.NewSessionStore(opts, cookieOpts)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("unknown session store type 'invalid-type'"))
			Expect(ss).To(BeNil())
		})
	})
})
