package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OpenAI5hWakeTask stores one durable administrator-triggered wake run.
type OpenAI5hWakeTask struct {
	ent.Schema
}

func (OpenAI5hWakeTask) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "openai_5h_wake_tasks"}}
}

func (OpenAI5hWakeTask) Fields() []ent.Field {
	return []ent.Field{
		field.String("trigger_type").MaxLen(32).Default("manual"),
		field.Int64("group_id").Optional().Nillable(),
		field.String("status").MaxLen(32).Default("pending"),
		field.Int("eligible_account_count").Default(0),
		field.Int("active_window_count").Default(0),
		field.Int("estimated_request_count").Default(0),
		field.Int("total_items").Default(0),
		field.Int("processed_items").Default(0),
		field.Int("woken_count").Default(0),
		field.Int("skipped_active_count").Default(0),
		field.Int("failed_count").Default(0),
		field.Int("cancelled_count").Default(0),
		field.Int64("requested_by_user_id").Optional().Nillable(),
		field.String("requested_by_email").Optional().Nillable().MaxLen(320),
		field.String("lease_owner").Optional().Nillable().MaxLen(128),
		field.Time("lease_expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("heartbeat_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("earliest_reset_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("latest_reset_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("cancel_requested_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("started_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("finished_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (OpenAI5hWakeTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("trigger_type", "status"),
		index.Fields("status"),
		index.Fields("created_at"),
		index.Fields("lease_expires_at"),
		index.Fields("finished_at"),
	}
}
