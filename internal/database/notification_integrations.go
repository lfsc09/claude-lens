package database

import (
	"context"
	"database/sql"
	"errors"
)

// NotificationTypeSlack is the only supported notification integration type.
const NotificationTypeSlack = "slack"

// NotificationIntegration is a row of notification_integrations: a
// destination limiter alerts can be sent to. Any number may be active.
type NotificationIntegration struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	WebhookURL string  `json:"-"`
	Active     bool    `json:"active"`
	CreatedAt  float64 `json:"created_at"`
	UpdatedAt  float64 `json:"updated_at"`
}

const notificationIntegrationColumns = `id, type, name, webhook_url, active, created_at, updated_at`

func scanNotificationIntegration(row interface{ Scan(...any) error }) (NotificationIntegration, error) {
	var n NotificationIntegration
	var active int
	err := row.Scan(&n.ID, &n.Type, &n.Name, &n.WebhookURL, &active, &n.CreatedAt, &n.UpdatedAt)
	n.Active = active != 0
	return n, err
}

// ListNotificationIntegrations returns every notification integration,
// oldest first.
func (db *DB) ListNotificationIntegrations(ctx context.Context) ([]NotificationIntegration, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+notificationIntegrationColumns+` FROM notification_integrations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	integrations := []NotificationIntegration{}
	for rows.Next() {
		n, err := scanNotificationIntegration(rows)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, n)
	}
	return integrations, rows.Err()
}

// GetNotificationIntegration returns the integration with the given id, or
// nil if it doesn't exist.
func (db *DB) GetNotificationIntegration(ctx context.Context, id int64) (*NotificationIntegration, error) {
	n, err := scanNotificationIntegration(db.sql.QueryRowContext(ctx,
		`SELECT `+notificationIntegrationColumns+` FROM notification_integrations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// CreateNotificationIntegration inserts a new integration and returns its id.
func (db *DB) CreateNotificationIntegration(ctx context.Context, n NotificationIntegration) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`INSERT INTO notification_integrations (type, name, webhook_url, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		n.Type, n.Name, n.WebhookURL, boolToInt(n.Active), n.CreatedAt, n.UpdatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateNotificationIntegration replaces an integration's name, type and
// webhook URL. Active state is SetNotificationIntegrationActive's job.
func (db *DB) UpdateNotificationIntegration(ctx context.Context, n NotificationIntegration) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE notification_integrations SET type = ?, name = ?, webhook_url = ?, updated_at = ? WHERE id = ?`,
		n.Type, n.Name, n.WebhookURL, n.UpdatedAt, n.ID,
	)
	return err
}

// SetNotificationIntegrationActive toggles whether alerts are delivered to
// id, keeping its limiter links.
func (db *DB) SetNotificationIntegrationActive(ctx context.Context, id int64, active bool, at float64) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE notification_integrations SET active = ?, updated_at = ? WHERE id = ?`, boolToInt(active), at, id)
	return err
}

// DeleteNotificationIntegration removes an integration and, by cascade, its
// limiter links. It is not an error if the id does not exist.
func (db *DB) DeleteNotificationIntegration(ctx context.Context, id int64) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM notification_integrations WHERE id = ?`, id)
	return err
}
