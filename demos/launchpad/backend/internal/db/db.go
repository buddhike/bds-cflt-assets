// Package db is the access to the single DynamoDB table.
//
// Callers describe conditions and updates as Go values, not expression strings.
// Dynamo turns them into DynamoDB expressions, and Memory evaluates them
// directly, so the logic that depends on conditional writes is tested without
// a database.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ErrConditionFailed means a conditional write did not apply.
var ErrConditionFailed = errors.New("the condition did not hold")

// Item is one row, as DynamoDB sees it.
type Item = map[string]types.AttributeValue

// DB is what the application needs from the table.
type DB interface {
	// Get returns the item, or nil if there is none. It reads consistently.
	Get(ctx context.Context, pk, sk string) (Item, error)
	// Put replaces an item. A nil cond always applies.
	Put(ctx context.Context, item Item, cond Cond) error
	// Update changes attributes of an item and returns it as it is afterwards.
	// DynamoDB creates the item if it is missing and cond allows it.
	Update(ctx context.Context, pk, sk string, u Update, cond Cond) (Item, error)
	Delete(ctx context.Context, pk, sk string, cond Cond) error
	// Query returns every item of a partition whose sort key starts with
	// skPrefix, in sort key order.
	Query(ctx context.Context, pk, skPrefix string) ([]Item, error)
}

// Update lists the changes of an Update call.
type Update struct {
	Set map[string]any   // attribute = value
	Add map[string]int64 // attribute = attribute + n. The attribute must exist.
}

// Cond is a condition on the item a write is about to change. A missing item
// has no attributes.
type Cond interface{ isCond() }

type (
	absent struct{ attr string }
	equal  struct {
		attr  string
		value any
	}
	less struct {
		attr  string
		value any
	}
	oneOf struct {
		attr   string
		values []any
	}
	and []Cond
	or  []Cond
)

func (absent) isCond() {}
func (equal) isCond()  {}
func (less) isCond()   {}
func (oneOf) isCond()  {}
func (and) isCond()    {}
func (or) isCond()     {}

// Absent holds when the item has no such attribute, which is also true when
// there is no item.
func Absent(attr string) Cond { return absent{attr} }

// Equal holds when the attribute equals value.
func Equal(attr string, value any) Cond { return equal{attr, value} }

// Less holds when the number attribute is below value.
func Less(attr string, value any) Cond { return less{attr, value} }

// OneOf holds when the attribute equals any of the values.
func OneOf(attr string, values ...any) Cond { return oneOf{attr, values} }

// And holds when every condition holds.
func And(conds ...Cond) Cond { return and(conds) }

// Or holds when any condition holds.
func Or(conds ...Cond) Cond { return or(conds) }

// Marshal turns a struct into an Item.
func Marshal(v any) (Item, error) {
	item, err := attributevalue.MarshalMap(v)
	if err != nil {
		return nil, fmt.Errorf("marshal item: %w", err)
	}
	return item, nil
}

// Unmarshal fills out, a pointer to a struct, from an Item.
func Unmarshal(item Item, out any) error {
	if err := attributevalue.UnmarshalMap(item, out); err != nil {
		return fmt.Errorf("unmarshal item: %w", err)
	}
	return nil
}

// Key is the primary key of an item.
func Key(pk, sk string) Item {
	return Item{"pk": &types.AttributeValueMemberS{Value: pk}, "sk": &types.AttributeValueMemberS{Value: sk}}
}

// GetAs reads an item into a T. It returns nil if there is none.
func GetAs[T any](ctx context.Context, d DB, pk, sk string) (*T, error) {
	item, err := d.Get(ctx, pk, sk)
	if err != nil || item == nil {
		return nil, err
	}
	out := new(T)
	if err := Unmarshal(item, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutAs writes a struct as an item.
func PutAs(ctx context.Context, d DB, v any, cond Cond) error {
	item, err := Marshal(v)
	if err != nil {
		return err
	}
	return d.Put(ctx, item, cond)
}

// UpdateAs applies an Update and reads the result into a T.
func UpdateAs[T any](ctx context.Context, d DB, pk, sk string, u Update, cond Cond) (*T, error) {
	item, err := d.Update(ctx, pk, sk, u, cond)
	if err != nil {
		return nil, err
	}
	out := new(T)
	if err := Unmarshal(item, out); err != nil {
		return nil, err
	}
	return out, nil
}

// QueryAs reads a partition into a slice of T.
func QueryAs[T any](ctx context.Context, d DB, pk, skPrefix string) ([]T, error) {
	items, err := d.Query(ctx, pk, skPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(items))
	for _, item := range items {
		var v T
		if err := Unmarshal(item, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
