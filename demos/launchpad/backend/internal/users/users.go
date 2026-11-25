// Package users is the user store.
//
// Sign-in is a code sent to one phone number, so there is one person who can sign
// in: the owner, whose address is the SuperUserEmail setting. The address is what
// names their saved credentials and the demos they launch.
package users

import (
	"context"
	"errors"

	"launchpad/internal/db"
	"launchpad/internal/services"
)

const Admin = "admin"

// User is a profile. Only the fields tagged for JSON reach the browser.
type User struct {
	PK        string `dynamodbav:"pk" json:"-"`
	SK        string `dynamodbav:"sk" json:"-"`
	Email     string `dynamodbav:"email" json:"email"`
	Role      string `dynamodbav:"role" json:"role"`
	CreatedAt int64  `dynamodbav:"createdAt" json:"createdAt"`
}

// PK is the partition key of a user's items.
func PK(email string) string { return "USER#" + email }

// Get returns the profile, or nil.
func Get(ctx context.Context, svc *services.Services, email string) (*User, error) {
	return db.GetAs[User](ctx, svc.DB, PK(email), "PROFILE")
}

// EnsureOwner returns the owner's profile, creating it on their first sign-in.
func EnsureOwner(ctx context.Context, svc *services.Services) (*User, error) {
	email := svc.Settings.SuperUserEmail
	user := &User{PK: PK(email), SK: "PROFILE", Email: email, Role: Admin, CreatedAt: svc.Now()}
	err := db.PutAs(ctx, svc.DB, user, db.Absent("pk"))
	if errors.Is(err, db.ErrConditionFailed) {
		existing, err := Get(ctx, svc, email)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, errors.New("the owner's profile vanished while it was being created")
		}
		return existing, nil
	}
	if err != nil {
		return nil, err
	}
	svc.Metrics.Count("UserRegistered")
	return user, nil
}
