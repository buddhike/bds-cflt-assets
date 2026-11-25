// Package sms sends the sign-in text message: SNS to the one phone number kept in Secrets Manager.
//
// The number is never taken from a request. The only way to change where codes
// go is to change the secret.
package sms

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"

	"launchpad/internal/apperr"
	"launchpad/internal/services"
)

var (
	e164  = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	strip = regexp.MustCompile(`[ \-.()]`)
)

func unavailable() *apperr.Error {
	return apperr.New(503, "sign_in_unavailable", "Sign-in is not set up. Put a phone number in the sign-in secret")
}

// numberFrom reads the number out of the secret. The secret is the number
// itself. A one-entry JSON object also works, which is what the console's
// key/value editor stores.
func numberFrom(raw string) string {
	text := strings.TrimSpace(raw)
	if !strings.HasPrefix(text, "{") {
		return text
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(text), &data); err != nil || len(data) != 1 {
		return ""
	}
	for _, v := range data {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// NormaliseNumber turns what a person typed into E.164, or a 400. Spaces,
// dashes, dots and brackets are dropped.
//
// This looks only at the input, never at the stored number, so a 400 says
// nothing about which number is right.
func NormaliseNumber(raw any) (string, error) {
	text, _ := raw.(string)
	text = strip.ReplaceAllString(text, "")
	if !e164.MatchString(text) {
		return "", apperr.BadRequest("invalid_number", "Enter your phone number with the country code, like +61400000000")
	}
	return text, nil
}

// SignInNumber is the phone number codes are sent to, in E.164 form such as +61400000000.
func SignInNumber(ctx context.Context, svc *services.Services) (string, error) {
	out, err := svc.Secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &svc.Settings.SignInSecret})
	if err != nil || out.SecretString == nil {
		svc.Log.Error("Could not read the sign-in secret", "error", err)
		return "", unavailable()
	}
	number := numberFrom(*out.SecretString)
	if !e164.MatchString(number) {
		// The value is not logged: it is the number.
		svc.Log.Error("The sign-in secret does not hold a phone number in E.164 format, such as +61400000000")
		return "", unavailable()
	}
	return number, nil
}

// SendCode texts the code to number.
func SendCode(ctx context.Context, svc *services.Services, number, otp string, minutes int) error {
	message := fmt.Sprintf("Launchpad sign-in code: %s. It works for %d minutes. If you did not ask for it, ignore this message.", otp, minutes)
	_, err := svc.SNS.Publish(ctx, &sns.PublishInput{
		PhoneNumber: aws.String(number),
		Message:     aws.String(message),
		MessageAttributes: map[string]snstypes.MessageAttributeValue{
			"AWS.SNS.SMS.SMSType": {DataType: aws.String("String"), StringValue: aws.String("Transactional")},
		},
	})
	if err != nil {
		// Do not tell the caller why: the reason can include account details.
		// The log gets it, without the number, which SNS may quote back.
		svc.Log.Error("Could not send the sign-in text message", "error", strings.ReplaceAll(err.Error(), number, "[number]"))
		return apperr.New(502, "sms_failed", "The sign-in code could not be sent. Try again shortly")
	}
	return nil
}
