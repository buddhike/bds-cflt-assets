package db

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// API is the part of the DynamoDB client that Dynamo uses.
type API interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// Dynamo is a DB backed by a DynamoDB table.
type Dynamo struct {
	api   API
	table string
}

// NewDynamo returns a DB for the named table.
func NewDynamo(api API, table string) *Dynamo { return &Dynamo{api: api, table: table} }

func (d *Dynamo) Get(ctx context.Context, pk, sk string) (Item, error) {
	out, err := d.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &d.table,
		Key:            Key(pk, sk),
		ConsistentRead: ptr(true),
	})
	if err != nil {
		return nil, fmt.Errorf("get item: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	return out.Item, nil
}

func (d *Dynamo) Put(ctx context.Context, item Item, cond Cond) error {
	in := &dynamodb.PutItemInput{TableName: &d.table, Item: item}
	if cond != nil {
		expr, err := conditionExpression(cond)
		if err != nil {
			return err
		}
		in.ConditionExpression = expr.Condition()
		in.ExpressionAttributeNames = expr.Names()
		in.ExpressionAttributeValues = expr.Values()
	}
	if _, err := d.api.PutItem(ctx, in); err != nil {
		return writeError("put item", err)
	}
	return nil
}

func (d *Dynamo) Update(ctx context.Context, pk, sk string, u Update, cond Cond) (Item, error) {
	builder := expression.NewBuilder().WithUpdate(updateBuilder(u))
	if cond != nil {
		builder = builder.WithCondition(conditionBuilder(cond))
	}
	expr, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("build update: %w", err)
	}
	out, err := d.api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &d.table,
		Key:                       Key(pk, sk),
		UpdateExpression:          expr.Update(),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		ReturnValues:              types.ReturnValueAllNew,
	})
	if err != nil {
		return nil, writeError("update item", err)
	}
	return out.Attributes, nil
}

func (d *Dynamo) Delete(ctx context.Context, pk, sk string, cond Cond) error {
	in := &dynamodb.DeleteItemInput{TableName: &d.table, Key: Key(pk, sk)}
	if cond != nil {
		expr, err := conditionExpression(cond)
		if err != nil {
			return err
		}
		in.ConditionExpression = expr.Condition()
		in.ExpressionAttributeNames = expr.Names()
		in.ExpressionAttributeValues = expr.Values()
	}
	if _, err := d.api.DeleteItem(ctx, in); err != nil {
		return writeError("delete item", err)
	}
	return nil
}

func (d *Dynamo) Query(ctx context.Context, pk, skPrefix string) ([]Item, error) {
	key := expression.Key("pk").Equal(expression.Value(pk))
	if skPrefix != "" {
		key = key.And(expression.Key("sk").BeginsWith(skPrefix))
	}
	expr, err := expression.NewBuilder().WithKeyCondition(key).Build()
	if err != nil {
		return nil, fmt.Errorf("build query: %w", err)
	}
	var items []Item
	var start Item
	for {
		out, err := d.api.Query(ctx, &dynamodb.QueryInput{
			TableName:                 &d.table,
			KeyConditionExpression:    expr.KeyCondition(),
			ExpressionAttributeNames:  expr.Names(),
			ExpressionAttributeValues: expr.Values(),
			ConsistentRead:            ptr(true),
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("query: %w", err)
		}
		items = append(items, out.Items...)
		if len(out.LastEvaluatedKey) == 0 {
			return items, nil
		}
		start = out.LastEvaluatedKey
	}
}

func writeError(what string, err error) error {
	var failed *types.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		return ErrConditionFailed
	}
	return fmt.Errorf("%s: %w", what, err)
}

func conditionExpression(c Cond) (expression.Expression, error) {
	expr, err := expression.NewBuilder().WithCondition(conditionBuilder(c)).Build()
	if err != nil {
		return expression.Expression{}, fmt.Errorf("build condition: %w", err)
	}
	return expr, nil
}

func conditionBuilder(c Cond) expression.ConditionBuilder {
	switch c := c.(type) {
	case absent:
		return expression.AttributeNotExists(expression.Name(c.attr))
	case equal:
		return expression.Name(c.attr).Equal(expression.Value(c.value))
	case less:
		return expression.Name(c.attr).LessThan(expression.Value(c.value))
	case oneOf:
		operands := make([]expression.OperandBuilder, len(c.values))
		for i, v := range c.values {
			operands[i] = expression.Value(v)
		}
		return expression.Name(c.attr).In(operands[0], operands[1:]...)
	case and:
		if len(c) == 1 {
			return conditionBuilder(c[0])
		}
		return expression.And(conditionBuilder(c[0]), conditionBuilder(c[1]), rest(c[2:])...)
	case or:
		if len(c) == 1 {
			return conditionBuilder(c[0])
		}
		return expression.Or(conditionBuilder(c[0]), conditionBuilder(c[1]), rest(c[2:])...)
	}
	panic(fmt.Sprintf("db: unknown condition %T", c))
}

func rest(conds []Cond) []expression.ConditionBuilder {
	out := make([]expression.ConditionBuilder, len(conds))
	for i, c := range conds {
		out[i] = conditionBuilder(c)
	}
	return out
}

func updateBuilder(u Update) expression.UpdateBuilder {
	var b expression.UpdateBuilder
	for _, name := range sortedKeys(u.Set) {
		b = b.Set(expression.Name(name), expression.Value(u.Set[name]))
	}
	for _, name := range sortedKeys(u.Add) {
		b = b.Set(expression.Name(name), expression.Name(name).Plus(expression.Value(u.Add[name])))
	}
	return b
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func ptr[T any](v T) *T { return &v }
