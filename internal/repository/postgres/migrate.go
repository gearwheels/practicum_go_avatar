package postgres

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // драйвер БД для миграций
	_ "github.com/golang-migrate/migrate/v4/source/file"       // источник миграций — файлы на диске
)

// RunMigrations применяет все миграции из директории migrationsPath к базе
// данных databaseURL. Идемпотентна: если схема уже актуальна, ничего не
// делает.
func RunMigrations(databaseURL, migrationsPath string) error {
	m, err := migrate.New("file://"+migrationsPath, databaseURL)
	if err != nil {
		return fmt.Errorf("создание мигратора: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("применение миграций: %w", err)
	}
	return nil
}
