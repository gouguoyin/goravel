package schema

import (
	"fmt"
	"slices"
	"strings"

	"github.com/goravel/framework/contracts/config"
	contractsdatabase "github.com/goravel/framework/contracts/database"
	contractsorm "github.com/goravel/framework/contracts/database/orm"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/database/schema/grammars"
	"github.com/goravel/framework/database/schema/processors"
	"github.com/goravel/framework/errors"
)

const BindingSchema = "goravel.schema"

var _ contractsschema.Schema = (*Schema)(nil)

type Schema struct {
	contractsschema.CommonSchema
	contractsschema.DriverSchema

	config     config.Config
	grammar    contractsschema.Grammar
	log        log.Log
	migrations []contractsschema.Migration
	orm        contractsorm.Orm
	prefix     string
	processor  contractsschema.Processor
	schema     string
}

func NewSchema(config config.Config, log log.Log, orm contractsorm.Orm, migrations []contractsschema.Migration) *Schema {
	driver := contractsdatabase.Driver(config.GetString(fmt.Sprintf("database.connections.%s.driver", orm.Name())))
	prefix := config.GetString(fmt.Sprintf("database.connections.%s.prefix", orm.Name()))
	var (
		driverSchema contractsschema.DriverSchema
		grammar      contractsschema.Grammar
		processor    contractsschema.Processor
		schema       string
	)

	switch driver {
	case contractsdatabase.DriverPostgres:
		schema = config.GetString(fmt.Sprintf("database.connections.%s.schema", orm.Name()), "public")

		postgresGrammar := grammars.NewPostgres(prefix)
		driverSchema = NewPostgresSchema(postgresGrammar, orm, schema, prefix)
		grammar = postgresGrammar
		processor = processors.NewPostgres()
	case contractsdatabase.DriverMysql:
		schema = config.GetString(fmt.Sprintf("database.connections.%s.database", orm.Name()))

		mysqlGrammar := grammars.NewMysql(prefix)
		driverSchema = NewMysqlSchema(mysqlGrammar, orm, prefix)
		grammar = mysqlGrammar
		processor = processors.NewMysql()
	case contractsdatabase.DriverSqlserver:
		sqlserverGrammar := grammars.NewSqlserver(prefix)
		driverSchema = NewSqlserverSchema(sqlserverGrammar, orm, prefix)
		grammar = sqlserverGrammar
		processor = processors.NewSqlserver()
	case contractsdatabase.DriverSqlite:
		sqliteGrammar := grammars.NewSqlite(log, prefix)
		driverSchema = NewSqliteSchema(sqliteGrammar, orm, prefix)
		grammar = sqliteGrammar
		processor = processors.NewSqlite()
	default:
		panic(errors.SchemaDriverNotSupported.Args(driver))
	}

	return &Schema{
		DriverSchema: driverSchema,
		CommonSchema: NewCommonSchema(grammar, orm),

		config:     config,
		grammar:    grammar,
		log:        log,
		migrations: migrations,
		orm:        orm,
		prefix:     prefix,
		processor:  processor,
		schema:     schema,
	}
}

func (r *Schema) Connection(name string) contractsschema.Schema {
	return NewSchema(r.config, r.log, r.orm.Connection(name), r.migrations)
}

func (r *Schema) Create(table string, callback func(table contractsschema.Blueprint)) error {
	blueprint := r.createBlueprint(table)
	blueprint.Create()
	callback(blueprint)

	if err := r.build(blueprint); err != nil {
		return errors.SchemaFailedToCreateTable.Args(table, err)
	}

	return nil
}

func (r *Schema) Drop(table string) error {
	blueprint := r.createBlueprint(table)
	blueprint.Drop()

	if err := r.build(blueprint); err != nil {
		return errors.SchemaFailedToDropTable.Args(table, err)
	}

	return nil
}

func (r *Schema) DropColumns(table string, columns []string) error {
	blueprint := r.createBlueprint(table)
	blueprint.DropColumn(columns...)

	if err := r.build(blueprint); err != nil {
		return errors.SchemaFailedToDropColumns.Args(table, err)
	}

	return nil
}

func (r *Schema) DropIfExists(table string) error {
	blueprint := r.createBlueprint(table)
	blueprint.DropIfExists()

	if err := r.build(blueprint); err != nil {
		return errors.SchemaFailedToDropTable.Args(table, err)
	}

	return nil
}

func (r *Schema) GetColumnListing(table string) []string {
	columns, err := r.GetColumns(table)
	if err != nil {
		r.log.Errorf("failed to get %s columns: %v", table, err)
		return nil
	}

	var names []string
	for _, column := range columns {
		names = append(names, column.Name)
	}

	return names
}

func (r *Schema) GetConnection() string {
	return r.orm.Name()
}

func (r *Schema) GetForeignKeys(table string) ([]contractsschema.ForeignKey, error) {
	table = r.prefix + table

	var dbForeignKeys []contractsschema.DBForeignKey
	if err := r.orm.Query().Raw(r.grammar.CompileForeignKeys(r.schema, table)).Scan(&dbForeignKeys); err != nil {
		return nil, err
	}

	return r.processor.ProcessForeignKeys(dbForeignKeys), nil
}

func (r *Schema) GetIndexListing(table string) []string {
	indexes, err := r.GetIndexes(table)
	if err != nil {
		r.log.Errorf("failed to get %s indexes: %v", table, err)
		return nil
	}

	var names []string
	for _, index := range indexes {
		names = append(names, index.Name)
	}

	return names
}

func (r *Schema) GetTableListing() []string {
	tables, err := r.GetTables()
	if err != nil {
		r.log.Errorf("failed to get tables: %v", err)
		return nil
	}

	var names []string
	for _, table := range tables {
		names = append(names, table.Name)
	}

	return names
}

func (r *Schema) HasColumn(table, column string) bool {
	return slices.Contains(r.GetColumnListing(table), column)
}

func (r *Schema) HasColumns(table string, columns []string) bool {
	columnListing := r.GetColumnListing(table)
	for _, column := range columns {
		if !slices.Contains(columnListing, column) {
			return false
		}
	}

	return true
}

func (r *Schema) HasIndex(table, index string) bool {
	indexListing := r.GetIndexListing(table)

	return slices.Contains(indexListing, index)
}

func (r *Schema) HasTable(name string) bool {
	var schema string
	if strings.Contains(name, ".") {
		lastDotIndex := strings.LastIndex(name, ".")
		schema = name[:lastDotIndex]
		name = name[lastDotIndex+1:]
	}

	tableName := r.prefix + name

	tables, err := r.GetTables()
	if err != nil {
		r.log.Errorf(errors.SchemaFailedToGetTables.Args(r.orm.Name(), err).Error())
		return false
	}

	for _, table := range tables {
		if table.Name == tableName {
			if schema == "" || schema == table.Schema {
				return true
			}
		}
	}

	return false
}

func (r *Schema) HasType(name string) bool {
	types, err := r.GetTypes()
	if err != nil {
		r.log.Errorf(errors.SchemaFailedToGetTables.Args(r.orm.Name(), err).Error())
		return false
	}

	for _, t := range types {
		if t.Name == name {
			return true
		}
	}

	return false
}

func (r *Schema) HasView(name string) bool {
	views, err := r.GetViews()
	if err != nil {
		r.log.Errorf(errors.SchemaFailedToGetTables.Args(r.orm.Name(), err).Error())
		return false
	}

	for _, view := range views {
		if view.Name == name {
			return true
		}
	}

	return false
}

func (r *Schema) Migrations() []contractsschema.Migration {
	return r.migrations
}

func (r *Schema) Orm() contractsorm.Orm {
	return r.orm
}

func (r *Schema) Register(migrations []contractsschema.Migration) {
	r.migrations = migrations
}

func (r *Schema) Rename(from, to string) error {
	blueprint := r.createBlueprint(from)
	blueprint.Rename(to)

	if err := r.build(blueprint); err != nil {
		return errors.SchemaFailedToRenameTable.Args(from, err)
	}

	return nil
}

func (r *Schema) SetConnection(name string) {
	r.orm = r.orm.Connection(name)
}

func (r *Schema) Sql(sql string) error {
	_, err := r.orm.Query().Exec(sql)

	return err
}

func (r *Schema) Table(table string, callback func(table contractsschema.Blueprint)) error {
	blueprint := r.createBlueprint(table)
	callback(blueprint)

	if err := r.build(blueprint); err != nil {
		return errors.SchemaFailedToChangeTable.Args(table, err)
	}

	return nil
}

func (r *Schema) build(blueprint contractsschema.Blueprint) error {
	if r.orm.Query().InTransaction() {
		return blueprint.Build(r.orm.Query(), r.grammar)
	}

	return r.orm.Transaction(func(tx contractsorm.Query) error {
		return blueprint.Build(tx, r.grammar)
	})
}

func (r *Schema) createBlueprint(table string) contractsschema.Blueprint {
	return NewBlueprint(r, r.prefix, table)
}
