package db

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	log "github.com/sirupsen/logrus"

	// this is needed to enable postgres database support
	_ "github.com/lib/pq"
)

// Client is the client object
type Client struct {
	db        *sqlx.DB
	ex        sqlx.Ext
	committed bool
}

// Config defines the config information to be passed to Connect method
type Config struct {
	User     string `default:"execution_tracker"`
	DBName   string `default:"execution_tracker"`
	Password string `envconfig:"execution_tracker_db_password" required:"true"`
	Host     string `envconfig:"postgres_host" default:"stolon-proxy.stolon"`
	Port     string `envconfig:"postgres_port" default:"5432"`
}

// NewClient creates a new client object
func NewClient(db *sqlx.DB) *Client {
	return &Client{
		db: db,
		ex: db,
	}
}

// Connect returns db client
func Connect(conf Config) (client *Client, err error) {
	cn := fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable",
		conf.Host,
		conf.Port,
		conf.DBName,
		conf.User,
		conf.Password)
	rawdb, err := sqlx.Connect("postgres", cn)
	if err != nil {
		log.Errorf("Failed to connect to postgres db: %s", err)
		return
	}
	client = NewClient(rawdb)
	return
}

// DB returns internal sqlx db connection
func (c *Client) DB() *sqlx.DB {
	return c.db
}

// currentTransaction returns the current transaction if there is one, otherwise nil
func (c *Client) currentTransaction() *sqlx.Tx {
	if tx, ok := c.ex.(*sqlx.Tx); ok {
		return tx
	}

	return nil
}

// Begin begins a transaction, and returns a client set up to use the transaction
func (c *Client) Begin() (*Client, error) {
	if tx := c.currentTransaction(); tx != nil {
		panic("can't start nested transaction")
	}

	tx, err := c.db.Beginx()
	if err != nil {
		return nil, err
	}

	return &Client{
		db: c.db,
		ex: tx,
	}, nil
}

// End ends a transaction, rolling the transaction back if it has not been committed
func (c *Client) End() {
	tx := c.currentTransaction()
	if !c.committed && tx == nil {
		panic("End() called outside transaction")
	}

	if !c.committed { // => tx != nil, or we'd have panicked above
		err := tx.Rollback()
		if err != nil {
			log.Errorf("Failed to rollback transaction: %s", err)
		}
	}

	c.ex = c.db
	c.committed = false
}

// Commit commits the current transaction
func (c *Client) Commit() error {
	tx := c.currentTransaction()
	if tx == nil {
		panic("Commit() called outside transaction")
	}

	err := tx.Commit()
	if err != nil {
		return err
	}

	c.ex = c.db
	c.committed = true
	return nil
}

// ExpireLogs deletes expired logs
func (c *Client) ExpireLogs() error {
	_, err := c.db.Exec(`DELETE FROM request_logs WHERE expires_at <= now()`)
	if err != nil {
		return err
	}

	_, err = c.db.Exec(`DELETE FROM system_logs WHERE expires_at <= now()`)
	if err != nil {
		return err
	}

	_, err = c.db.Exec(`DELETE FROM customer_logs WHERE expires_at <= now()`)
	if err != nil {
		return err
	}

	return nil
}
