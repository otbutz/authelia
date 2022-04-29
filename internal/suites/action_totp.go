package suites

import (
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (rs *RodSession) doRegisterTOTP(t *testing.T, page *rod.Page) string {
	err := rs.WaitElementLocatedByID(t, page, "register-link").Click("left")
	require.NoError(t, err)
	rs.verifyMailNotificationDisplayed(t, page)
	link := doGetLinkFromLastMail(t)
	rs.doVisit(t, page, link)
	secretURL, err := page.MustElement("#secret-url").Attribute("value")
	assert.NoError(t, err)

	secret := (*secretURL)[strings.LastIndex(*secretURL, "=")+1:]
	assert.NotEqual(t, "", secret)
	assert.NotNil(t, secret)

	return secret
}

func (rs *RodSession) doEnterOTP(t *testing.T, page *rod.Page, code string) {
	inputs := rs.WaitElementsLocatedByID(t, page, "otp-input input")

	if len(inputs) != len(code) {
		rs.collectScreenshot(page)
	}

	require.Len(t, inputs, len(code))

	for i := 0; i < len(code); i++ {
		_ = inputs[i].MustPress(rune(code[i]))
	}
}

func (rs *RodSession) doValidateTOTP(t *testing.T, page *rod.Page, secret string) {
	code, err := totp.GenerateCode(secret, time.Now())
	assert.NoError(t, err)
	rs.doEnterOTP(t, page, code)
}
