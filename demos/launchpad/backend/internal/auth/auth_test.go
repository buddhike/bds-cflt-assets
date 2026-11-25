package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"launchpad/internal/apperr"
	"launchpad/internal/auth"
	"launchpad/internal/db"
	"launchpad/internal/sms"
	"launchpad/internal/testenv"
	"launchpad/internal/users"
)

var ctx = context.Background()

const resend = auth.ResendSeconds * time.Second

func signIn(t *testing.T, env *testenv.Env) *auth.Result {
	t.Helper()
	if err := auth.Start(ctx, env.Services, testenv.OwnerPhone); err != nil {
		t.Fatal(err)
	}
	res, err := auth.Verify(ctx, env.Services, env.SNS.LastCode())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func start(t *testing.T, env *testenv.Env, number any) {
	t.Helper()
	if err := auth.Start(ctx, env.Services, number); err != nil {
		t.Fatalf("Start(%v): %v", number, err)
	}
}

func wantAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	appErr, ok := apperr.As(err)
	if !ok || appErr.Status != status || appErr.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}

func numbers(env *testenv.Env) []string {
	var out []string
	for _, s := range env.SNS.Sent() {
		out = append(out, s.Number)
	}
	return out
}

func TestCodeGoesToTheOwnerAndSignsThemIn(t *testing.T) {
	env := testenv.New(t)
	res := signIn(t, env)
	if got := numbers(env); len(got) != 1 || got[0] != testenv.OwnerPhone {
		t.Fatalf("sent to %v", got)
	}
	if res.User.Email != testenv.OwnerEmail || res.User.Role != "admin" {
		t.Fatalf("user %+v", res.User)
	}
	user, err := auth.SessionUser(ctx, env.Services, res.Token)
	if err != nil || user.Email != testenv.OwnerEmail {
		t.Fatalf("session user %+v, %v", user, err)
	}
}

// --- the number the page asks for -------------------------------------------

func TestAnotherNumberSendsNothingAndLooksTheSame(t *testing.T) {
	env := testenv.New(t)
	start(t, env, "+61400000099") // no error, no message
	if n := len(env.SNS.Sent()); n != 0 {
		t.Fatalf("a number that is not the owner's sent %d messages", n)
	}
	start(t, env, testenv.OwnerPhone)
	if got := numbers(env); len(got) != 1 || got[0] != testenv.OwnerPhone {
		t.Fatalf("sent to %v", got)
	}
}

func TestAnotherNumberLeavesNoTraceThatCouldReplaceACode(t *testing.T) {
	env := testenv.New(t)
	start(t, env, testenv.OwnerPhone)
	code := env.SNS.LastCode()
	env.Advance(resend + time.Second)
	for range 20 {
		start(t, env, "+61400000099")
	}
	if n := len(env.SNS.Sent()); n != 1 {
		t.Fatalf("%d messages sent, want 1", n)
	}
	if _, err := auth.Verify(ctx, env.Services, code); err != nil {
		t.Fatalf("the code no longer works: %v", err)
	}
}

func TestTheNumberMayBeTypedWithSpacesAndDashes(t *testing.T) {
	for _, typed := range []string{"+61 400 000 001", "+61-400-000-001", " +61(400)000.001 ", "+61400000001"} {
		t.Run(typed, func(t *testing.T) {
			env := testenv.New(t)
			start(t, env, typed)
			if got := numbers(env); len(got) != 1 || got[0] != testenv.OwnerPhone {
				t.Fatalf("sent to %v", got)
			}
		})
	}
}

func TestSomethingThatIsNotAPhoneNumberIsA400(t *testing.T) {
	typed := []any{"", "0400000001", "61400000001", "+0400000001", "abc", "+61400", "+" + strings.Repeat("9", 20), nil, 61400000001.0, []any{"+61400000001"}}
	for _, v := range typed {
		env := testenv.New(t)
		wantAPIError(t, auth.Start(ctx, env.Services, v), 400, "invalid_number")
		if len(env.SNS.Sent()) != 0 {
			t.Fatalf("%v sent a message", v)
		}
	}
}

func TestThe400DoesNotDependOnTheStoredNumber(t *testing.T) {
	env := testenv.New(t)
	first := auth.Start(ctx, env.Services, "0400")
	env.Secrets.Put(testenv.SecretID, "+61400000002")
	second := auth.Start(ctx, env.Services, "0400")
	a, _ := apperr.As(first)
	b, _ := apperr.As(second)
	if a == nil || b == nil || *a != *b {
		t.Fatalf("answers differ: %v, %v", first, second)
	}
}

func TestASecretThatIsNotSetUpStillLooksLikeASend(t *testing.T) {
	env := testenv.New(t)
	env.Secrets.Put(testenv.SecretID, "REPLACE_WITH_A_PHONE_NUMBER")
	start(t, env, testenv.OwnerPhone)
	if len(env.SNS.Sent()) != 0 {
		t.Fatal("a message was sent")
	}
	if item, _ := env.Table.Get(ctx, "OTP#owner", "OTP"); item != nil {
		t.Fatal("a code was stored")
	}
}

func TestAFailedSendStillLooksLikeASend(t *testing.T) {
	env := testenv.New(t)
	env.SNS.Err = errors.New("AuthorizationError: account 123 may not publish to " + testenv.OwnerPhone)
	start(t, env, testenv.OwnerPhone)
	if strings.Contains(env.Logs(), testenv.OwnerPhone) {
		t.Fatalf("the number is in the log: %s", env.Logs())
	}
	if !strings.Contains(env.Logs(), "[number]") {
		t.Fatalf("the SNS error is not in the log: %s", env.Logs())
	}
}

type failingPuts struct{ db.DB }

func (failingPuts) Put(context.Context, db.Item, db.Cond) error { return errors.New("table is gone") }

func TestACrashStillLooksLikeASend(t *testing.T) {
	env := testenv.New(t)
	env.DB = failingPuts{env.Table}
	start(t, env, testenv.OwnerPhone)
	if !strings.Contains(env.Emitted(), `"reason":"error"`) {
		t.Fatalf("not counted: %s", env.Emitted())
	}
}

func TestEveryReasonForNotSendingIsCounted(t *testing.T) {
	env := testenv.New(t)
	start(t, env, "+61400000099")
	start(t, env, testenv.OwnerPhone)
	start(t, env, testenv.OwnerPhone) // held back by the 30 second limit
	emitted := env.Emitted()
	for _, want := range []string{`"reason":"number_mismatch"`, `"reason":"too_soon"`} {
		if !strings.Contains(emitted, want) {
			t.Errorf("no %s in %s", want, emitted)
		}
	}
	for _, secret := range []string{"+61400000099", testenv.OwnerPhone} {
		if strings.Contains(env.Logs(), secret) || strings.Contains(emitted, secret) {
			t.Errorf("%s is in the logs or metrics", secret)
		}
	}
	if !strings.Contains(env.Logs(), "Sign-in code not sent") {
		t.Errorf("the log line the README names is missing: %s", env.Logs())
	}
}

// --- the code ---------------------------------------------------------------

func TestAWrongCodeIsRejectedAndCounted(t *testing.T) {
	env := testenv.New(t)
	start(t, env, testenv.OwnerPhone)
	wrong := "000000"
	if env.SNS.LastCode() == wrong {
		wrong = "111111"
	}
	for range auth.MaxGuesses {
		if _, err := auth.Verify(ctx, env.Services, wrong); err == nil {
			t.Fatal("a wrong code was accepted")
		}
	}
	// After the limit even the right code fails.
	if _, err := auth.Verify(ctx, env.Services, env.SNS.LastCode()); err == nil {
		t.Fatal("the right code was accepted after too many guesses")
	}
	if got := strings.Count(env.Emitted(), `"SignInFailed":1`); got < auth.MaxGuesses {
		t.Errorf("%d failures counted, want at least %d", got, auth.MaxGuesses)
	}
}

func TestNoCodeCanBeGuessedBeforeOneIsSent(t *testing.T) {
	env := testenv.New(t)
	_, err := auth.Verify(ctx, env.Services, "123456")
	wantAPIError(t, err, 400, "invalid_code")
}

func TestACodeIsSingleUseAndExpires(t *testing.T) {
	env := testenv.New(t)
	start(t, env, testenv.OwnerPhone)
	code := env.SNS.LastCode()
	if _, err := auth.Verify(ctx, env.Services, code); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Verify(ctx, env.Services, code); err == nil {
		t.Fatal("a code worked twice")
	}
	env.Advance(resend)
	start(t, env, testenv.OwnerPhone)
	code = env.SNS.LastCode()
	env.Advance(auth.OTPMinutes * time.Minute)
	if _, err := auth.Verify(ctx, env.Services, code); err == nil {
		t.Fatal("an expired code worked")
	}
}

func TestANewCodeReplacesTheOldOne(t *testing.T) {
	env := testenv.New(t)
	start(t, env, testenv.OwnerPhone)
	old := env.SNS.LastCode()
	env.Advance(resend)
	start(t, env, testenv.OwnerPhone)
	if n := len(env.SNS.Sent()); n != 2 {
		t.Fatalf("%d messages, want 2", n)
	}
	if old != env.SNS.LastCode() {
		if _, err := auth.Verify(ctx, env.Services, old); err == nil {
			t.Fatal("the old code still works")
		}
	}
	if _, err := auth.Verify(ctx, env.Services, env.SNS.LastCode()); err != nil {
		t.Fatalf("the new code does not work: %v", err)
	}
}

func TestResendAndHourlyLimits(t *testing.T) {
	env := testenv.New(t)
	start(t, env, testenv.OwnerPhone)
	start(t, env, testenv.OwnerPhone) // too soon: no second message, and no error either
	if n := len(env.SNS.Sent()); n != 1 {
		t.Fatalf("%d messages, want 1", n)
	}
	for range auth.MaxSendsPerHour - 1 {
		env.Advance(resend)
		start(t, env, testenv.OwnerPhone)
	}
	if n := len(env.SNS.Sent()); n != auth.MaxSendsPerHour {
		t.Fatalf("%d messages, want %d", n, auth.MaxSendsPerHour)
	}
	env.Advance(resend)
	start(t, env, testenv.OwnerPhone)
	if n := len(env.SNS.Sent()); n != auth.MaxSendsPerHour {
		t.Fatalf("the hourly limit did not hold: %d messages", n)
	}
	env.Advance(time.Hour)
	start(t, env, testenv.OwnerPhone)
	if n := len(env.SNS.Sent()); n != auth.MaxSendsPerHour+1 {
		t.Fatalf("a new hour must allow a message: %d messages", n)
	}
}

func TestTheCodeFormatIsCheckedBeforeAnythingIsRead(t *testing.T) {
	env := testenv.New(t)
	env.DB = failingEverything{}
	for _, bad := range []any{"12345", "abcdef", "1234567", nil, 123456.0, "１２３４５６"} {
		_, err := auth.Verify(ctx, env.Services, bad)
		wantAPIError(t, err, 400, "invalid_code")
	}
}

type failingEverything struct{ db.DB }

func (failingEverything) Get(context.Context, string, string) (db.Item, error) {
	return nil, errors.New("the table must not be read")
}

func TestTheCodeIsNotStoredInClear(t *testing.T) {
	env := testenv.New(t)
	start(t, env, testenv.OwnerPhone)
	item, _ := env.Table.Get(ctx, "OTP#owner", "OTP")
	code := env.SNS.LastCode()
	for name, v := range item {
		if text, ok := v.(*types.AttributeValueMemberS); ok && strings.Contains(text.Value, code) {
			t.Errorf("attribute %s holds the code", name)
		}
	}
	if item["hash"] == nil || item["salt"] == nil {
		t.Errorf("no salted hash stored: %v", item)
	}
}

func TestSessionExpiryAndLogout(t *testing.T) {
	env := testenv.New(t)
	token := signIn(t, env).Token
	if _, err := auth.SessionUser(ctx, env.Services, token); err != nil {
		t.Fatal(err)
	}
	if err := auth.Logout(ctx, env.Services, token); err != nil {
		t.Fatal(err)
	}
	_, err := auth.SessionUser(ctx, env.Services, token)
	wantAPIError(t, err, 401, "unauthorized")

	env.Advance(resend)
	token = signIn(t, env).Token
	env.Advance(time.Duration(env.Settings.SessionHours) * time.Hour)
	if _, err := auth.SessionUser(ctx, env.Services, token); err == nil {
		t.Fatal("an expired session worked")
	}
	if _, err := auth.SessionUser(ctx, env.Services, ""); err == nil {
		t.Fatal("no token worked")
	}
	if err := auth.Logout(ctx, env.Services, ""); err != nil {
		t.Fatalf("logging out without a session: %v", err)
	}
}

func TestANewOwnerAddressEndsTheOldSessions(t *testing.T) {
	env := testenv.New(t)
	token := signIn(t, env).Token
	env.Settings.SuperUserEmail = "other@confluent.io"
	_, err := auth.SessionUser(ctx, env.Services, token)
	wantAPIError(t, err, 401, "unauthorized")
}

func TestTheOwnerProfileIsCreatedOnce(t *testing.T) {
	env := testenv.New(t)
	signIn(t, env)
	created, _ := users.Get(ctx, env.Services, testenv.OwnerEmail)
	env.Advance(100 * time.Second)
	signIn(t, env)
	again, _ := users.Get(ctx, env.Services, testenv.OwnerEmail)
	if created == nil || again == nil || created.CreatedAt != again.CreatedAt {
		t.Fatalf("created %+v, then %+v", created, again)
	}
	if got := strings.Count(env.Emitted(), `"UserRegistered":1`); got != 1 {
		t.Fatalf("UserRegistered counted %d times", got)
	}
}

// --- the real text message ------------------------------------------------

func TestTheTextGoesToTheNumberInTheSecretAndOnlyThere(t *testing.T) {
	env := testenv.New(t)
	start(t, env, testenv.OwnerPhone)
	sent := env.SNS.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d messages", len(sent))
	}
	in := sent[0].Input
	if *in.PhoneNumber != testenv.OwnerPhone || in.TopicArn != nil || in.TargetArn != nil {
		t.Fatalf("destination %+v", in)
	}
	if got := *in.MessageAttributes["AWS.SNS.SMS.SMSType"].StringValue; got != "Transactional" {
		t.Fatalf("SMS type %q", got)
	}
	if sent[0].Code() == "" {
		t.Fatalf("no six digit code in %q", sent[0].Message)
	}
}

func TestTheSecretIsReadOnEverySend(t *testing.T) {
	env := testenv.New(t)
	start(t, env, testenv.OwnerPhone)
	env.Secrets.Put(testenv.SecretID, "+61400000002")
	env.Advance(resend)
	start(t, env, "+61400000002")
	if got := numbers(env); len(got) != 2 || got[0] != testenv.OwnerPhone || got[1] != "+61400000002" {
		t.Fatalf("sent to %v", got)
	}
}

func TestAOneEntryJSONSecretIsAccepted(t *testing.T) {
	env := testenv.New(t)
	env.Secrets.Put(testenv.SecretID, `{"phone": " +61400000003 "}`)
	start(t, env, "+61400000003")
	if got := numbers(env); len(got) != 1 || got[0] != "+61400000003" {
		t.Fatalf("sent to %v", got)
	}
}

func TestASecretThatIsNotANumberIsA503(t *testing.T) {
	for _, value := range []string{"REPLACE_WITH_A_PHONE_NUMBER", " ", "0400000000", "+61 400 000 001", "+0123456", `{"a": "+61400000001", "b": "+61400000002"}`, "{not json", "[1]", `{"a": 5}`} {
		t.Run(value, func(t *testing.T) {
			env := testenv.New(t)
			env.Secrets.Put(testenv.SecretID, value)
			_, err := sms.SignInNumber(ctx, env.Services)
			wantAPIError(t, err, 503, "sign_in_unavailable")
		})
	}
}

func TestAnUnreadableSecretIsA503(t *testing.T) {
	env := testenv.New(t)
	env.Settings.SignInSecret = "no-such-secret"
	_, err := sms.SignInNumber(ctx, env.Services)
	wantAPIError(t, err, 503, "sign_in_unavailable")
}

func TestAFailedSendIsA502WithoutDetail(t *testing.T) {
	env := testenv.New(t)
	env.SNS.Err = errors.New("AuthorizationError: account 123 may not publish to " + testenv.OwnerPhone)
	err := sms.SendCode(ctx, env.Services, testenv.OwnerPhone, "123456", 10)
	wantAPIError(t, err, 502, "sms_failed")
	if msg := err.Error(); strings.Contains(msg, "+614") || strings.Contains(msg, "123") {
		t.Fatalf("the message gives details: %q", msg)
	}
}

func TestSendCodeWritesTheCodeAndTheLifetime(t *testing.T) {
	env := testenv.New(t)
	if err := sms.SendCode(ctx, env.Services, testenv.OwnerPhone, "654321", 10); err != nil {
		t.Fatal(err)
	}
	msg := env.SNS.Sent()[0].Message
	if !strings.Contains(msg, "654321") || !strings.Contains(msg, "10 minutes") {
		t.Fatalf("message %q", msg)
	}
}

type panicsOnPut struct{ db.DB }

func (panicsOnPut) Put(context.Context, db.Item, db.Cond) error { panic("nil map write") }

func TestAPanicStillLooksLikeASend(t *testing.T) {
	env := testenv.New(t)
	env.DB = panicsOnPut{env.Table}
	start(t, env, testenv.OwnerPhone) // must return nil, as for every other reason
	if !strings.Contains(env.Emitted(), `"reason":"error"`) {
		t.Fatalf("not counted: %s", env.Emitted())
	}
}
