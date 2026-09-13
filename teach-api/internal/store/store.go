// Package store owns the SQLite representation of the public teaching
// directory. It deliberately has no model for private grades or account data.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	defaultPage     = 1
	defaultPageSize = 24
	maxPageSize     = 100
)

var (
	emailPattern     = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
	hashEmailPattern = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+#[A-Z0-9.\-]+\.[A-Z]{2,}`)
	phonePattern     = regexp.MustCompile(`(?:\+?86[-\s]?)?1[3-9][0-9]{9}|0[0-9]{2,3}[-\s][0-9]{7,8}`)
	encryptedContactPattern = regexp.MustCompile(`(?i)^[0-9a-f]{64,}$`)
)

// Snapshot is the JSON handoff produced by teach-crawler. Unknown fields are
// ignored so adding crawler diagnostics does not widen the public API.
type Snapshot struct {
	SchemaVersion int               `json:"schema_version"`
	GeneratedAt   string            `json:"generated_at"`
	Source        map[string]string `json:"source"`
	Stats         SnapshotStats     `json:"stats"`
	Teachers      []SnapshotTeacher `json:"teachers"`
}

type SnapshotStats struct {
	ListPages int `json:"list_pages"`
}

type SnapshotTeacher struct {
	Name             string            `json:"name"`
	DirectoryName    string            `json:"directory_name,omitempty"`
	ProfileName      string            `json:"profile_name,omitempty"`
	ProfileURL       string            `json:"profile_url"`
	Initial          string            `json:"initial"`
	DirectoryNum     int               `json:"directory_num,omitempty"`
	NameMatch        bool              `json:"name_match"`
	Position         string            `json:"position,omitempty"`
	SupervisorRoles  string            `json:"supervisor_roles,omitempty"`
	College          string            `json:"college,omitempty"`
	PrimaryRole      string            `json:"primary_role,omitempty"`
	EntryDate        string            `json:"entry_date,omitempty"`
	EmploymentStatus string            `json:"employment_status,omitempty"`
	Education        string            `json:"education,omitempty"`
	Degree           string            `json:"degree,omitempty"`
	University       string            `json:"university,omitempty"`
	Gender           string            `json:"gender,omitempty"`
	Email            string            `json:"email,omitempty"`
	Phone            string            `json:"phone,omitempty"`
	OfficeLocation   string            `json:"office_location,omitempty"`
	PostalCode       string            `json:"postal_code,omitempty"`
	Introduction     string            `json:"introduction,omitempty"`
	Research         []string          `json:"research_directions,omitempty"`
	AvatarURL        string            `json:"avatar_url,omitempty"`
	Sections         []SnapshotSection `json:"sections,omitempty"`
	Status           string            `json:"status"`
}

type SnapshotSection struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Teacher is the public API representation. It omits crawler status, parser
// errors and raw HTML; explicitly labelled public contact fields are exposed
// as structured values.
type Teacher struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	ProfileURL       string           `json:"profile_url"`
	Initial          string           `json:"initial,omitempty"`
	DirectoryNum     int              `json:"directory_num,omitempty"`
	Position         string           `json:"position,omitempty"`
	SupervisorRoles  string           `json:"supervisor_roles,omitempty"`
	College          string           `json:"college,omitempty"`
	PrimaryRole      string           `json:"primary_role,omitempty"`
	EntryDate        string           `json:"entry_date,omitempty"`
	EmploymentStatus string           `json:"employment_status,omitempty"`
	Education        string           `json:"education,omitempty"`
	Degree           string           `json:"degree,omitempty"`
	University       string           `json:"university,omitempty"`
	Gender           string           `json:"gender,omitempty"`
	Email            string           `json:"email,omitempty"`
	Phone            string           `json:"phone,omitempty"`
	OfficeLocation   string           `json:"office_location,omitempty"`
	PostalCode       string           `json:"postal_code,omitempty"`
	Introduction     string           `json:"introduction,omitempty"`
	Research         []string         `json:"research_directions,omitempty"`
	AvatarURL        string           `json:"avatar_url,omitempty"`
	Sections         []TeacherSection `json:"sections,omitempty"`
}

// TeacherSummary is the compact representation used by the directory list.
// Long-form sections and contact fields are returned only by the detail API so
// a paginated directory remains small and cache-friendly.
type TeacherSummary struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ProfileURL       string   `json:"profile_url"`
	Initial          string   `json:"initial,omitempty"`
	DirectoryNum     int      `json:"directory_num,omitempty"`
	Position         string   `json:"position,omitempty"`
	SupervisorRoles  string   `json:"supervisor_roles,omitempty"`
	College          string   `json:"college,omitempty"`
	PrimaryRole      string   `json:"primary_role,omitempty"`
	EmploymentStatus string   `json:"employment_status,omitempty"`
	Education        string   `json:"education,omitempty"`
	Degree           string   `json:"degree,omitempty"`
	University       string   `json:"university,omitempty"`
	Introduction     string   `json:"introduction,omitempty"`
	Research         []string `json:"research_directions,omitempty"`
	AvatarURL        string   `json:"avatar_url,omitempty"`
}

type TeacherSection struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// CoursesSnapshot is the JSON handoff produced from the registrar's Excel
// export (see scripts/xlsx_to_courses.py). Teacher names are resolved to
// directory IDs at import time so the snapshot stays portable.
type CoursesSnapshot struct {
	SchemaVersion int              `json:"schema_version"`
	GeneratedAt   string           `json:"generated_at"`
	Term          string           `json:"term"`
	Courses       []SnapshotCourse `json:"courses"`
}

type SnapshotCourse struct {
	ID           string  `json:"id"`
	Term         string  `json:"term"`
	College      string  `json:"college,omitempty"`
	CourseCode   string  `json:"course_code,omitempty"`
	CourseName   string  `json:"course_name"`
	ClassNum     string  `json:"class_num,omitempty"`
	TeacherName  string  `json:"teacher_name"`
	TeacherTitle string  `json:"teacher_title,omitempty"`
	Credit       float64 `json:"credit,omitempty"`
	HoursTotal   int     `json:"hours_total,omitempty"`
	HoursWeek    int     `json:"hours_week,omitempty"`
	Weeks        string  `json:"weeks,omitempty"`
	Weekday      string  `json:"weekday,omitempty"`
	Periods      string  `json:"periods,omitempty"`
	Campus       string  `json:"campus,omitempty"`
	ScheduleText string  `json:"schedule_text,omitempty"`
	Assessment   string  `json:"assessment,omitempty"`
	Nature       string  `json:"nature,omitempty"`
	Category     string  `json:"category,omitempty"`
	Remark       string  `json:"remark,omitempty"`
}

// Course is the public API representation of one teaching class (教学班).
type Course struct {
	ID           string  `json:"id"`
	Term         string  `json:"term"`
	College      string  `json:"college,omitempty"`
	CourseCode   string  `json:"course_code,omitempty"`
	CourseName   string  `json:"course_name"`
	ClassNum     string  `json:"class_num,omitempty"`
	TeacherName  string  `json:"teacher_name"`
	TeacherTitle string  `json:"teacher_title,omitempty"`
	TeacherID    string  `json:"teacher_id,omitempty"`
	Credit       float64 `json:"credit,omitempty"`
	HoursTotal   int     `json:"hours_total,omitempty"`
	HoursWeek    int     `json:"hours_week,omitempty"`
	Weeks        string  `json:"weeks,omitempty"`
	Weekday      string  `json:"weekday,omitempty"`
	Periods      string  `json:"periods,omitempty"`
	Campus       string  `json:"campus,omitempty"`
	ScheduleText string  `json:"schedule_text,omitempty"`
	Assessment   string  `json:"assessment,omitempty"`
	Nature       string      `json:"nature,omitempty"`
	Category     string      `json:"category,omitempty"`
	Remark       string      `json:"remark,omitempty"`
	Huoshui      *HuoshuiRef `json:"huoshui"`
}

// CourseSummary is the compact representation used by the course list and the
// teacher profile's teaching section.
type CourseSummary struct {
	ID          string      `json:"id"`
	Term        string      `json:"term"`
	College     string      `json:"college,omitempty"`
	CourseCode  string      `json:"course_code,omitempty"`
	CourseName  string      `json:"course_name"`
	ClassNum    string      `json:"class_num,omitempty"`
	TeacherName string      `json:"teacher_name"`
	TeacherID   string      `json:"teacher_id,omitempty"`
	Credit      float64     `json:"credit,omitempty"`
	Weekday     string      `json:"weekday,omitempty"`
	Periods     string      `json:"periods,omitempty"`
	Campus      string      `json:"campus,omitempty"`
	Nature      string      `json:"nature,omitempty"`
	Category    string      `json:"category,omitempty"`
	Huoshui     *HuoshuiRef `json:"huoshui"`
}

// CourseGroupSummary is the list-row representation of one course: 不同老师、
// 不同教学班的同一门课聚合成一行,避免“一门课只显示一个老师”的误导。
// 列表不附带活水评分——评分因老师而异,聚合行上显示任何一个都不准确。
type CourseGroupSummary struct {
	CourseCode   string   `json:"course_code"`
	CourseName   string   `json:"course_name"`
	College      string   `json:"college,omitempty"`
	Credit       float64  `json:"credit,omitempty"`
	Term         string   `json:"term,omitempty"`
	ClassCount   int      `json:"class_count"`
	TeacherNames []string `json:"teacher_names,omitempty"`
	Campuses     []string `json:"campuses,omitempty"`
	Weekdays     []string `json:"weekdays,omitempty"`
	Natures      []string `json:"natures,omitempty"`
	Categories   []string `json:"categories,omitempty"`
}

type CourseGroupPage struct {
	Data       []CourseGroupSummary `json:"data"`
	Pagination Pagination           `json:"pagination"`
}

type CourseFilter struct {
	Page     int
	PageSize int
	Search   string
	Campus   string
	Weekday  string
	College  string
	Category string
	Term     string // 空 = 最新学期;"all" = 全部学期;其余按学期精确匹配
}

type DatasetMeta struct {
	Version            string `json:"data_version"`
	GeneratedAt        string `json:"generated_at,omitempty"`
	SourceSite         string `json:"source_site,omitempty"`
	BaseURL            string `json:"source_base_url,omitempty"`
	RecordCount        int    `json:"record_count"`
	ListPages          int    `json:"list_pages"`
	ImportedAt         string `json:"imported_at,omitempty"`
	CoursesVersion     string `json:"courses_version,omitempty"`
	CoursesTerm        string `json:"courses_term,omitempty"`
	CourseCount        int    `json:"course_count"`
	HuoshuiVersion     string `json:"huoshui_version,omitempty"`
	HuoshuiImportedAt  string `json:"huoshui_imported_at,omitempty"`
	HuoshuiCourseCount int    `json:"huoshui_course_count,omitempty"`
	HuoshuiReviewCount int    `json:"huoshui_review_count,omitempty"`
	HuoshuiMatchCount  int    `json:"huoshui_match_count,omitempty"`
}

type TeacherFilter struct {
	Page     int
	PageSize int
	Search   string
	Initial  string
	College  string
}

type TeacherPage struct {
	Data       []TeacherSummary `json:"data"`
	Pagination Pagination       `json:"pagination"`
}

type Pagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

var ErrNotFound = errors.New("teacher not found")

type Store struct {
	db *sql.DB

	metaMu sync.RWMutex
	meta   DatasetMeta
	loaded bool
}

func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("database path cannot be empty")
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(8)
	}
	store := &Store{db: db}
	if err := store.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.loadMeta(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) configure() error {
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 30000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS teachers (
	id TEXT PRIMARY KEY NOT NULL,
	profile_url TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	directory_name TEXT NOT NULL DEFAULT '',
	profile_name TEXT NOT NULL DEFAULT '',
	initial TEXT NOT NULL DEFAULT '',
	directory_num INTEGER NOT NULL DEFAULT 0,
	name_match INTEGER NOT NULL DEFAULT 0,
	position TEXT NOT NULL DEFAULT '',
	supervisor_roles TEXT NOT NULL DEFAULT '',
	college TEXT NOT NULL DEFAULT '',
	primary_role TEXT NOT NULL DEFAULT '',
	entry_date TEXT NOT NULL DEFAULT '',
	employment_status TEXT NOT NULL DEFAULT '',
	education TEXT NOT NULL DEFAULT '',
	degree TEXT NOT NULL DEFAULT '',
	university TEXT NOT NULL DEFAULT '',
	gender TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT '',
	phone TEXT NOT NULL DEFAULT '',
	office_location TEXT NOT NULL DEFAULT '',
	postal_code TEXT NOT NULL DEFAULT '',
	introduction TEXT NOT NULL DEFAULT '',
	research_directions_json TEXT NOT NULL DEFAULT '[]',
	avatar_url TEXT NOT NULL DEFAULT '',
	sections_json TEXT NOT NULL DEFAULT '[]',
	source_status TEXT NOT NULL DEFAULT 'ok',
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS teachers_name_idx ON teachers(name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS teachers_initial_idx ON teachers(initial);
CREATE INDEX IF NOT EXISTS teachers_college_idx ON teachers(college);
CREATE TABLE IF NOT EXISTS dataset_meta (
	key TEXT PRIMARY KEY NOT NULL,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS courses (
	id TEXT NOT NULL,
	term TEXT NOT NULL DEFAULT '',
	college TEXT NOT NULL DEFAULT '',
	course_code TEXT NOT NULL DEFAULT '',
	course_name TEXT NOT NULL,
	class_num TEXT NOT NULL DEFAULT '',
	teacher_name TEXT NOT NULL,
	teacher_title TEXT NOT NULL DEFAULT '',
	teacher_id TEXT NOT NULL DEFAULT '',
	credit REAL NOT NULL DEFAULT 0,
	hours_total INTEGER NOT NULL DEFAULT 0,
	hours_week INTEGER NOT NULL DEFAULT 0,
	weeks TEXT NOT NULL DEFAULT '',
	weekday TEXT NOT NULL DEFAULT '',
	periods TEXT NOT NULL DEFAULT '',
	campus TEXT NOT NULL DEFAULT '',
	schedule_text TEXT NOT NULL DEFAULT '',
	assessment TEXT NOT NULL DEFAULT '',
	nature TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT '',
	remark TEXT NOT NULL DEFAULT '',
	shuffle_key INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (course_code, id, term)
);
CREATE INDEX IF NOT EXISTS courses_teacher_id_idx ON courses(teacher_id);
CREATE INDEX IF NOT EXISTS courses_name_idx ON courses(course_name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS courses_campus_idx ON courses(campus);
CREATE INDEX IF NOT EXISTS courses_weekday_idx ON courses(weekday);
CREATE INDEX IF NOT EXISTS courses_college_idx ON courses(college);
CREATE INDEX IF NOT EXISTS courses_code_idx ON courses(course_code);
CREATE TABLE IF NOT EXISTS huoshui_courses (
	object_id TEXT PRIMARY KEY NOT NULL,
	name TEXT NOT NULL,
	prof TEXT NOT NULL,
	dept TEXT NOT NULL DEFAULT '',
	position TEXT NOT NULL DEFAULT '',
	review_count INTEGER NOT NULL DEFAULT 0,
	rate_overall REAL NOT NULL DEFAULT 0,
	rate1 REAL NOT NULL DEFAULT 0,
	rate2 REAL NOT NULL DEFAULT 0,
	rate3 REAL NOT NULL DEFAULT 0,
	attendance_overall REAL NOT NULL DEFAULT 0,
	attendance_count INTEGER NOT NULL DEFAULT 0,
	homework_overall REAL NOT NULL DEFAULT 0,
	homework_count INTEGER NOT NULL DEFAULT 0,
	exam_overall REAL NOT NULL DEFAULT 0,
	exam_count INTEGER NOT NULL DEFAULT 0,
	bird_overall REAL NOT NULL DEFAULT 0,
	bird_count INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS huoshui_courses_name_prof_idx ON huoshui_courses(name, prof);
CREATE INDEX IF NOT EXISTS huoshui_courses_review_count_idx ON huoshui_courses(review_count);
CREATE INDEX IF NOT EXISTS huoshui_courses_dept_idx ON huoshui_courses(dept);
CREATE TABLE IF NOT EXISTS huoshui_reviews (
	object_id TEXT PRIMARY KEY NOT NULL,
	course_name TEXT NOT NULL,
	prof_name TEXT NOT NULL,
	comment TEXT NOT NULL DEFAULT '',
	rating_overall REAL NOT NULL DEFAULT 0,
	up_vote INTEGER NOT NULL DEFAULT 0,
	exam_info TEXT NOT NULL DEFAULT '',
	attendance TEXT NOT NULL DEFAULT '',
	bird TEXT NOT NULL DEFAULT '',
	homework TEXT NOT NULL DEFAULT '',
	author_dept TEXT NOT NULL DEFAULT '',
	author_year TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS huoshui_reviews_course_idx ON huoshui_reviews(course_name, prof_name);
CREATE INDEX IF NOT EXISTS huoshui_reviews_up_vote_idx ON huoshui_reviews(up_vote);
CREATE TABLE IF NOT EXISTS huoshui_match (
	registrar_course_id TEXT NOT NULL,
	term TEXT NOT NULL DEFAULT '',
	huoshui_course_id TEXT NOT NULL REFERENCES huoshui_courses(object_id),
	PRIMARY KEY (term, registrar_course_id)
);
CREATE INDEX IF NOT EXISTS huoshui_match_huoshui_idx ON huoshui_match(huoshui_course_id);`)
	if err != nil {
		return fmt.Errorf("create sqlite schema: %w", err)
	}
	if err := s.ensureTeacherColumns(); err != nil {
		return err
	}
	if err := s.ensureCourseColumns(); err != nil {
		return err
	}
	if err := s.migrateCourseSchema(); err != nil {
		return err
	}
	return nil
}

// migrateCourseSchema upgrades an existing database to the composite course
// primary key (course_code, id, term). teachId 每学期都会重新分配,单列主键
// 无法跨学期共存;迁移后同一门课的历史学期可以并存。huoshui_match 的学期列
// 同理,由于它由 courses 派生,直接重建后由导入流程重新填充。
func (s *Store) migrateCourseSchema() error {
	coursePK := make(map[string]int)
	rows, err := s.db.Query(`PRAGMA table_info(courses)`)
	if err != nil {
		return fmt.Errorf("inspect courses primary key: %w", err)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan courses primary key: %w", err)
		}
		coursePK[name] = pk
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close courses primary key: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin course schema migration: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	rebuiltCourses := false
	if coursePK["course_code"] == 0 {
		statements := []string{
			`CREATE TABLE courses_migrated (
	id TEXT NOT NULL,
	term TEXT NOT NULL DEFAULT '',
	college TEXT NOT NULL DEFAULT '',
	course_code TEXT NOT NULL DEFAULT '',
	course_name TEXT NOT NULL,
	class_num TEXT NOT NULL DEFAULT '',
	teacher_name TEXT NOT NULL,
	teacher_title TEXT NOT NULL DEFAULT '',
	teacher_id TEXT NOT NULL DEFAULT '',
	credit REAL NOT NULL DEFAULT 0,
	hours_total INTEGER NOT NULL DEFAULT 0,
	hours_week INTEGER NOT NULL DEFAULT 0,
	weeks TEXT NOT NULL DEFAULT '',
	weekday TEXT NOT NULL DEFAULT '',
	periods TEXT NOT NULL DEFAULT '',
	campus TEXT NOT NULL DEFAULT '',
	schedule_text TEXT NOT NULL DEFAULT '',
	assessment TEXT NOT NULL DEFAULT '',
	nature TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT '',
	remark TEXT NOT NULL DEFAULT '',
	shuffle_key INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (course_code, id, term)
)`,
			`INSERT INTO courses_migrated (
	id, term, college, course_code, course_name, class_num,
	teacher_name, teacher_title, teacher_id, credit, hours_total, hours_week,
	weeks, weekday, periods, campus, schedule_text, assessment, nature,
	category, remark, shuffle_key, updated_at
) SELECT
	id, term, college, course_code, course_name, class_num,
	teacher_name, teacher_title, teacher_id, credit, hours_total, hours_week,
	weeks, weekday, periods, campus, schedule_text, assessment, nature,
	category, remark, shuffle_key, updated_at
FROM courses`,
			`DROP TABLE courses`,
			`ALTER TABLE courses_migrated RENAME TO courses`,
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				rollback()
				return fmt.Errorf("migrate courses primary key: %w", err)
			}
		}
		rebuiltCourses = true
	}
	// 老库的 huoshui_match 没有 term 列时直接重建(内容可由课程数据派生)。
	matchHasTerm := false
	matchRows, err := tx.Query(`PRAGMA table_info(huoshui_match)`)
	if err != nil {
		rollback()
		return fmt.Errorf("inspect huoshui_match schema: %w", err)
	}
	for matchRows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := matchRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			_ = matchRows.Close()
			rollback()
			return fmt.Errorf("scan huoshui_match schema: %w", err)
		}
		if name == "term" {
			matchHasTerm = true
		}
	}
	if err := matchRows.Close(); err != nil {
		rollback()
		return fmt.Errorf("close huoshui_match schema: %w", err)
	}
	rebuiltMatch := false
	if !matchHasTerm {
		statements := []string{
			`DROP TABLE huoshui_match`,
			`CREATE TABLE huoshui_match (
	registrar_course_id TEXT NOT NULL,
	term TEXT NOT NULL DEFAULT '',
	huoshui_course_id TEXT NOT NULL REFERENCES huoshui_courses(object_id),
	PRIMARY KEY (term, registrar_course_id)
)`,
			`CREATE INDEX IF NOT EXISTS huoshui_match_huoshui_idx ON huoshui_match(huoshui_course_id)`,
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				rollback()
				return fmt.Errorf("migrate huoshui_match schema: %w", err)
			}
		}
		rebuiltMatch = true
	}
	if rebuiltCourses {
		indexes := []string{
			`CREATE INDEX IF NOT EXISTS courses_teacher_id_idx ON courses(teacher_id)`,
			`CREATE INDEX IF NOT EXISTS courses_name_idx ON courses(course_name COLLATE NOCASE)`,
			`CREATE INDEX IF NOT EXISTS courses_campus_idx ON courses(campus)`,
			`CREATE INDEX IF NOT EXISTS courses_weekday_idx ON courses(weekday)`,
			`CREATE INDEX IF NOT EXISTS courses_college_idx ON courses(college)`,
			`CREATE INDEX IF NOT EXISTS courses_code_idx ON courses(course_code)`,
			`CREATE INDEX IF NOT EXISTS courses_shuffle_idx ON courses(shuffle_key)`,
		}
		for _, statement := range indexes {
			if _, err := tx.Exec(statement); err != nil {
				rollback()
				return fmt.Errorf("recreate course indexes: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		rollback()
		return fmt.Errorf("commit course schema migration: %w", err)
	}
	if rebuiltMatch {
		// 匹配表被清空重建,立刻按现有课程与活水数据重新关联。
		if err := s.rebuildHuoshuiMatchesIfPresent(context.Background()); err != nil {
			return fmt.Errorf("rebuild huoshui matches after migration: %w", err)
		}
	}
	return nil
}

// ensureCourseColumns keeps an existing public database forward-compatible
// when the courses table gains another column.
func (s *Store) ensureCourseColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(courses)`)
	if err != nil {
		return fmt.Errorf("inspect course schema: %w", err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan course schema: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read course schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close course schema: %w", err)
	}
	if !columns["shuffle_key"] {
		if _, err := s.db.Exec(`ALTER TABLE courses ADD COLUMN shuffle_key INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add course column shuffle_key: %w", err)
		}
	}
	// 索引依赖 shuffle_key 列,必须在老库补列之后创建。
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS courses_shuffle_idx ON courses(shuffle_key)`); err != nil {
		return fmt.Errorf("create course shuffle index: %w", err)
	}
	return nil
}

// ensureTeacherColumns keeps an existing public database forward-compatible
// when the profile page gains another structured field.
func (s *Store) ensureTeacherColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(teachers)`)
	if err != nil {
		return fmt.Errorf("inspect teacher schema: %w", err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan teacher schema: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("read teacher schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close teacher schema: %w", err)
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{name: "entry_date", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "primary_role", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "email", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "phone", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "office_location", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "postal_code", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "avatar_url", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "sections_json", def: "TEXT NOT NULL DEFAULT '[]'"},
	} {
		if columns[column.name] {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE teachers ADD COLUMN ` + column.name + ` ` + column.def); err != nil {
			return fmt.Errorf("add teacher column %q: %w", column.name, err)
		}
	}
	return nil
}

func (s *Store) loadMeta(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM dataset_meta`)
	if err != nil {
		return fmt.Errorf("read dataset metadata: %w", err)
	}
	defer rows.Close()
	var meta DatasetMeta
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Errorf("scan dataset metadata: %w", err)
		}
		switch key {
		case "version":
			meta.Version = value
		case "generated_at":
			meta.GeneratedAt = value
		case "source_site":
			meta.SourceSite = value
		case "source_base_url":
			meta.BaseURL = value
		case "record_count":
			meta.RecordCount, _ = strconv.Atoi(value)
		case "list_pages":
			meta.ListPages, _ = strconv.Atoi(value)
		case "imported_at":
			meta.ImportedAt = value
		case "courses_version":
			meta.CoursesVersion = value
		case "courses_term":
			meta.CoursesTerm = value
		case "course_count":
			meta.CourseCount, _ = strconv.Atoi(value)
		case "huoshui_version":
			meta.HuoshuiVersion = value
		case "huoshui_imported_at":
			meta.HuoshuiImportedAt = value
		case "huoshui_course_count":
			meta.HuoshuiCourseCount, _ = strconv.Atoi(value)
		case "huoshui_review_count":
			meta.HuoshuiReviewCount, _ = strconv.Atoi(value)
		case "huoshui_match_count":
			meta.HuoshuiMatchCount, _ = strconv.Atoi(value)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read dataset metadata: %w", err)
	}
	s.metaMu.Lock()
	s.meta = meta
	s.loaded = meta.Version != ""
	s.metaMu.Unlock()
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Meta() DatasetMeta {
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	return s.meta
}

func (s *Store) HasDataset() bool {
	s.metaMu.RLock()
	defer s.metaMu.RUnlock()
	return s.loaded
}

func (s *Store) ImportJSON(ctx context.Context, path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	return s.ImportBytes(ctx, contents)
}

func (s *Store) ImportBytes(ctx context.Context, contents []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var snapshot Snapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	rows, err := buildRows(snapshot)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(contents)
	version := hex.EncodeToString(digest[:])
	importedAt := time.Now().UTC().Format(time.RFC3339)
	sourceSite := strings.TrimSpace(snapshot.Source["site"])
	baseURL := strings.TrimSpace(snapshot.Source["base_url"])
	meta := DatasetMeta{
		Version:     version,
		GeneratedAt: strings.TrimSpace(snapshot.GeneratedAt),
		SourceSite:  sourceSite,
		BaseURL:     baseURL,
		RecordCount: len(rows),
		ListPages:   snapshot.Stats.ListPages,
		ImportedAt:  importedAt,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin snapshot import: %w", err)
	}
	rollback := func() {
		_ = tx.Rollback()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM teachers`); err != nil {
		rollback()
		return fmt.Errorf("replace teachers: %w", err)
	}
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO teachers (
	id, profile_url, name, directory_name, profile_name, initial,
	directory_num, name_match, position, supervisor_roles, college,
	primary_role, entry_date, employment_status, education, degree, university, gender,
	email, phone, office_location, postal_code, introduction, research_directions_json,
	avatar_url, sections_json, source_status, updated_at
) VALUES (
 ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
 ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)`)
	if err != nil {
		rollback()
		return fmt.Errorf("prepare teacher import: %w", err)
	}
	for _, row := range rows {
		if _, err := statement.ExecContext(ctx,
			row.ID, row.ProfileURL, row.Name, row.DirectoryName, row.ProfileName,
			row.Initial, row.DirectoryNum, row.NameMatch, row.Position,
			row.SupervisorRoles, row.College, row.PrimaryRole, row.EntryDate, row.EmploymentStatus,
			row.Education, row.Degree, row.University, row.Gender, row.Email,
			row.Phone, row.OfficeLocation, row.PostalCode, row.Introduction, row.ResearchJSON,
			row.AvatarURL, row.SectionsJSON, row.Status, importedAt,
		); err != nil {
			_ = statement.Close()
			rollback()
			return fmt.Errorf("insert teacher %q: %w", row.Name, err)
		}
	}
	if err := statement.Close(); err != nil {
		rollback()
		return fmt.Errorf("close teacher import: %w", err)
	}
	for key, value := range map[string]string{
		"version":         meta.Version,
		"generated_at":    meta.GeneratedAt,
		"source_site":     meta.SourceSite,
		"source_base_url": meta.BaseURL,
		"record_count":    strconv.Itoa(meta.RecordCount),
		"list_pages":      strconv.Itoa(meta.ListPages),
		"imported_at":     meta.ImportedAt,
	} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO dataset_meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			rollback()
			return fmt.Errorf("write dataset metadata: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		rollback()
		return fmt.Errorf("commit snapshot import: %w", err)
	}
	s.metaMu.Lock()
	s.meta = meta
	s.loaded = true
	s.metaMu.Unlock()
	return nil
}

// ImportCoursesJSON replaces the courses table with a registrar snapshot and
// resolves teacher names against the current teacher directory.
func (s *Store) ImportCoursesJSON(ctx context.Context, path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read courses snapshot: %w", err)
	}
	return s.ImportCoursesBytes(ctx, contents)
}

func (s *Store) ImportCoursesBytes(ctx context.Context, contents []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var snapshot CoursesSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		return fmt.Errorf("decode courses snapshot: %w", err)
	}
	if len(snapshot.Courses) == 0 {
		return errors.New("courses snapshot is empty")
	}
	teacherIDs, err := s.teacherIDsByName(ctx)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(contents)
	version := hex.EncodeToString(digest[:])
	importedAt := time.Now().UTC().Format(time.RFC3339)
	term := strings.TrimSpace(snapshot.Term)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin courses import: %w", err)
	}
	rollback := func() {
		_ = tx.Rollback()
	}
	// 只替换本快照覆盖的学期,其他学期的归档数据保留。
	snapshotTerms := make(map[string]struct{})
	for _, course := range snapshot.Courses {
		courseTerm := strings.TrimSpace(course.Term)
		if courseTerm == "" {
			courseTerm = term
		}
		snapshotTerms[courseTerm] = struct{}{}
	}
	for snapshotTerm := range snapshotTerms {
		if _, err := tx.ExecContext(ctx, `DELETE FROM courses WHERE term = ?`, snapshotTerm); err != nil {
			rollback()
			return fmt.Errorf("replace courses for term %q: %w", snapshotTerm, err)
		}
	}
	// 顺序按快照内容做确定性洗牌:同一快照顺序稳定(分页不出错),数据更新后重洗。
	shuffleKeys := make([]int, len(snapshot.Courses))
	for index := range shuffleKeys {
		shuffleKeys[index] = index
	}
	rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64([]byte(version[:8])))))
	rng.Shuffle(len(shuffleKeys), func(i, j int) { shuffleKeys[i], shuffleKeys[j] = shuffleKeys[j], shuffleKeys[i] })

	statement, err := tx.PrepareContext(ctx, `
INSERT INTO courses (
	id, term, college, course_code, course_name, class_num,
	teacher_name, teacher_title, teacher_id, credit, hours_total, hours_week,
	weeks, weekday, periods, campus, schedule_text, assessment, nature,
	category, remark, shuffle_key, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)`)
	if err != nil {
		rollback()
		return fmt.Errorf("prepare course import: %w", err)
	}
	seen := make(map[string]struct{}, len(snapshot.Courses))
	for index, course := range snapshot.Courses {
		id := strings.TrimSpace(course.ID)
		name := sanitizePublicText(course.CourseName)
		teacherName := sanitizePublicText(course.TeacherName)
		if id == "" || name == "" || teacherName == "" {
			_ = statement.Close()
			rollback()
			return fmt.Errorf("course at index %d is missing id, course_name or teacher_name", index)
		}
		if _, exists := seen[id]; exists {
			_ = statement.Close()
			rollback()
			return fmt.Errorf("duplicate course id %q", id)
		}
		seen[id] = struct{}{}
		courseTerm := strings.TrimSpace(course.Term)
		if courseTerm == "" {
			courseTerm = term
		}
		if _, err := statement.ExecContext(ctx,
			id, courseTerm, sanitizePublicText(course.College), sanitizePublicText(course.CourseCode),
			name, sanitizePublicText(course.ClassNum), teacherName, sanitizePublicText(course.TeacherTitle),
			teacherIDs[teacherName], course.Credit, course.HoursTotal, course.HoursWeek,
			sanitizePublicText(course.Weeks), sanitizePublicText(course.Weekday), sanitizePublicText(course.Periods),
			sanitizePublicText(course.Campus), sanitizePublicText(course.ScheduleText), sanitizePublicText(course.Assessment),
			sanitizePublicText(course.Nature), canonicalizeCategory(course.Category), sanitizePublicText(course.Remark),
			shuffleKeys[index],
			importedAt,
		); err != nil {
			_ = statement.Close()
			rollback()
			return fmt.Errorf("insert course %q: %w", id, err)
		}
	}
	if err := statement.Close(); err != nil {
		rollback()
		return fmt.Errorf("close course import: %w", err)
	}
	if term == "" {
		row := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT term) FROM courses`)
		var terms int
		if err := row.Scan(&terms); err == nil && terms == 1 {
			_ = tx.QueryRowContext(ctx, `SELECT term FROM courses LIMIT 1`).Scan(&term)
		}
	}
	for key, value := range map[string]string{
		"courses_version": version,
		"courses_term":    term,
		"course_count":    strconv.Itoa(len(seen)),
	} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO dataset_meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			rollback()
			return fmt.Errorf("write courses metadata: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		rollback()
		return fmt.Errorf("commit courses import: %w", err)
	}
	if err := s.rebuildHuoshuiMatchesIfPresent(ctx); err != nil {
		return fmt.Errorf("rebuild huoshui matches: %w", err)
	}
	if err := s.loadMeta(ctx); err != nil {
		return fmt.Errorf("reload metadata: %w", err)
	}
	return nil
}

// rebuildHuoshuiMatchesIfPresent refreshes the registrar-course ↔ huoshui
// links after a courses import; with no huoshui data imported it is a no-op.
func (s *Store) rebuildHuoshuiMatchesIfPresent(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM huoshui_courses`).Scan(&count); err != nil {
		return fmt.Errorf("check huoshui data: %w", err)
	}
	if count == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin huoshui match rebuild: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM huoshui_match`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear huoshui matches: %w", err)
	}
	matches, err := rebuildHuoshuiMatches(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO dataset_meta (key, value) VALUES ('huoshui_match_count', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.Itoa(matches)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("write huoshui match count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit huoshui match rebuild: %w", err)
	}
	return nil
}

// teacherIDsByName maps directory names to teacher IDs. Names shared by more
// than one teacher stay unmapped so a course never links to the wrong person.
func (s *Store) teacherIDsByName(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, directory_name FROM teachers`)
	if err != nil {
		return nil, fmt.Errorf("load teacher names: %w", err)
	}
	defer rows.Close()
	owners := make(map[string]string)
	ambiguous := make(map[string]bool)
	claim := func(name, id string) {
		if name == "" || ambiguous[name] {
			return
		}
		if existing, taken := owners[name]; taken && existing != id {
			ambiguous[name] = true
			delete(owners, name)
			return
		}
		owners[name] = id
	}
	for rows.Next() {
		var id, name, directoryName string
		if err := rows.Scan(&id, &name, &directoryName); err != nil {
			return nil, fmt.Errorf("scan teacher name: %w", err)
		}
		claim(name, id)
		claim(directoryName, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate teacher names: %w", err)
	}
	return owners, nil
}

func (s *Store) ListCourses(ctx context.Context, filter CourseFilter) (CourseGroupPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	page := filter.Page
	if page < defaultPage {
		page = defaultPage
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	where, args := courseWhere(
		sanitizeQuery(filter.Search),
		sanitizeQuery(filter.Campus),
		sanitizeQuery(filter.Weekday),
		sanitizeQuery(filter.College),
		sanitizeQuery(filter.Category),
		sanitizeQuery(filter.Term),
	)
	// 课程代码为空的兜底按课程名分组(正常数据没有空代码,防御旧快照)。
	const groupKey = `CASE WHEN course_code = '' THEN 'name:' || course_name ELSE 'code:' || course_code END`
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT `+groupKey+`) FROM courses WHERE `+where, args...).Scan(&total); err != nil {
		return CourseGroupPage{}, fmt.Errorf("count courses: %w", err)
	}
	offset := (page - 1) * pageSize
	// 先按课程代码分页(组内最小 shuffle_key 决定组的顺序,同一快照内稳定),
	// 再取回这些课程的全部教学班做聚合。
	codeRows, err := s.db.QueryContext(ctx, `
SELECT `+groupKey+`, MIN(shuffle_key) FROM courses
WHERE `+where+`
GROUP BY `+groupKey+`
ORDER BY MIN(shuffle_key) ASC, `+groupKey+` ASC
LIMIT ? OFFSET ?`, append(args, pageSize, offset)...)
	if err != nil {
		return CourseGroupPage{}, fmt.Errorf("list course codes: %w", err)
	}
	keys := make([]string, 0, pageSize)
	for codeRows.Next() {
		var key string
		var minShuffle int
		if err := codeRows.Scan(&key, &minShuffle); err != nil {
			_ = codeRows.Close()
			return CourseGroupPage{}, fmt.Errorf("scan course code: %w", err)
		}
		keys = append(keys, key)
	}
	if err := codeRows.Err(); err != nil {
		_ = codeRows.Close()
		return CourseGroupPage{}, fmt.Errorf("iterate course codes: %w", err)
	}
	if err := codeRows.Close(); err != nil {
		return CourseGroupPage{}, fmt.Errorf("close course codes: %w", err)
	}

	summaries := make([]CourseGroupSummary, 0, len(keys))
	if len(keys) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
		keyArgs := make([]any, 0, len(keys))
		for _, key := range keys {
			keyArgs = append(keyArgs, key)
		}
		classRows, err := s.db.QueryContext(ctx, `
SELECT `+groupKey+`, course_code, course_name, college, credit, term, teacher_name, campus, weekday, nature, category
FROM courses WHERE `+groupKey+` IN (`+placeholders+`) AND `+where+`
ORDER BY shuffle_key ASC, id ASC`, append(keyArgs, args...)...)
		if err != nil {
			return CourseGroupPage{}, fmt.Errorf("list grouped courses: %w", err)
		}
		defer classRows.Close()
		byKey := make(map[string]*CourseGroupSummary, len(keys))
		seen := make(map[string]map[string]bool, len(keys))
		for classRows.Next() {
			var key, code, name, college, term, teacher, campus, weekday, nature, category string
			var credit float64
			if err := classRows.Scan(&key, &code, &name, &college, &credit, &term, &teacher, &campus, &weekday, &nature, &category); err != nil {
				return CourseGroupPage{}, fmt.Errorf("scan grouped course: %w", err)
			}
			group, ok := byKey[key]
			if !ok {
				group = &CourseGroupSummary{CourseCode: code, CourseName: name, College: college, Credit: credit, Term: term}
				byKey[key] = group
				seen[key] = make(map[string]bool)
			}
			group.ClassCount++
			marks := seen[key]
			if teacher != "" && !marks["t:"+teacher] {
				marks["t:"+teacher] = true
				group.TeacherNames = append(group.TeacherNames, teacher)
			}
			if campus != "" && !marks["c:"+campus] {
				marks["c:"+campus] = true
				group.Campuses = append(group.Campuses, campus)
			}
			if weekday != "" && !marks["w:"+weekday] {
				marks["w:"+weekday] = true
				group.Weekdays = append(group.Weekdays, weekday)
			}
			if nature != "" && !marks["n:"+nature] {
				marks["n:"+nature] = true
				group.Natures = append(group.Natures, nature)
			}
			// 班级名单("XX2026-01班")不是真分类,聚合行不展示。
			for _, token := range strings.Split(category, ",") {
				token = strings.TrimSpace(token)
				if token == "" || strings.Contains(token, "班") || marks["g:"+token] {
					continue
				}
				marks["g:"+token] = true
				group.Categories = append(group.Categories, token)
			}
		}
		if err := classRows.Err(); err != nil {
			return CourseGroupPage{}, fmt.Errorf("iterate grouped courses: %w", err)
		}
		for _, key := range keys {
			summaries = append(summaries, *byKey[key])
		}
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return CourseGroupPage{
		Data:       summaries,
		Pagination: Pagination{Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages},
	}, nil
}

func courseWhere(search, campus, weekday, college, category, term string) (string, []any) {
	conditions := []string{"1 = 1"}
	args := make([]any, 0, 8)
	switch term {
	case "":
		// 默认只看最新学期(导入时写入 dataset_meta 的 courses_term)。
		conditions = append(conditions, `term = (SELECT value FROM dataset_meta WHERE key = 'courses_term')`)
	case "all":
	default:
		conditions = append(conditions, `term = ?`)
		args = append(args, term)
	}
	if search != "" {
		pattern := likePattern(search)
		conditions = append(conditions, `(course_name LIKE ? ESCAPE '\' OR course_code LIKE ? ESCAPE '\' OR teacher_name LIKE ? ESCAPE '\' OR college LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	if campus != "" {
		conditions = append(conditions, `campus = ?`)
		args = append(args, campus)
	}
	if weekday != "" {
		conditions = append(conditions, `weekday = ?`)
		args = append(args, weekday)
	}
	if college != "" {
		conditions = append(conditions, `college LIKE ? ESCAPE '\'`)
		args = append(args, likePattern(college))
	}
	if category != "" {
		// category 在导入时已规范化为逗号连接,直接按完整 token 匹配。
		conditions = append(conditions, `(',' || category || ',') LIKE ? ESCAPE '\'`)
		args = append(args, likePattern(","+category+","))
	}
	return strings.Join(conditions, " AND "), args
}

// protectedCategoryNames 是官方通识分类名,名字本身含有"、",
// 规范化拆分时必须原样保留。
var protectedCategoryNames = []string{
	"交通、工程与创新世界",
	"历史、文化与人文情怀",
	"艺术体验与审美修养",
	"自然科学与科学精神",
	"社会科学与责任伦理",
	"生态环境与生命关怀",
}

// canonicalizeCategory normalizes a raw category cell into comma-joined tokens:
// enrolment class groups and real categories are separated by ",", "，", "、" or
// "/" in the source data, with protectedCategoryNames kept intact.
func canonicalizeCategory(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// 受保护的完整分类名先换成占位符,避免被顿号拆开。
	masked := raw
	for index, name := range protectedCategoryNames {
		if strings.Contains(masked, name) {
			masked = strings.ReplaceAll(masked, name, fmt.Sprintf("\x00%d\x00", index))
		}
	}
	tokens := make([]string, 0, 4)
	seen := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(masked, func(r rune) bool {
		return r == ',' || r == '，' || r == '、' || r == '/'
	}) {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		for index, name := range protectedCategoryNames {
			token = strings.ReplaceAll(token, fmt.Sprintf("\x00%d\x00", index), name)
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	return strings.Join(tokens, ",")
}

// ListCourseCategories returns the distinct category tokens across courses.
// Class-group values like "半导体2026-01班" are enrolment lists, not real
// categories, and are excluded so the list stays useful as a filter.
func (s *Store) ListCourseCategories(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT category FROM courses WHERE category <> '' AND term = (SELECT value FROM dataset_meta WHERE key = 'courses_term')`)
	if err != nil {
		return nil, fmt.Errorf("list course categories: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	categories := make([]string, 0, 16)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan course category: %w", err)
		}
		for _, token := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '，' }) {
			token = strings.TrimSpace(token)
			if token == "" || strings.Contains(token, "班") {
				continue
			}
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			categories = append(categories, token)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate course categories: %w", err)
	}
	sort.Strings(categories)
	return categories, nil
}

// ListCourseTerms returns the distinct terms across courses, newest first.
// Term strings like "2026-2027第1学期" sort chronologically as plain text.
func (s *Store) ListCourseTerms(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT term FROM courses WHERE term <> '' ORDER BY term DESC`)
	if err != nil {
		return nil, fmt.Errorf("list course terms: %w", err)
	}
	defer rows.Close()
	terms := make([]string, 0, 4)
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			return nil, fmt.Errorf("scan course term: %w", err)
		}
		terms = append(terms, term)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate course terms: %w", err)
	}
	return terms, nil
}

func (s *Store) GetCourse(ctx context.Context, id string) (Course, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Course{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, term, college, course_code, course_name, class_num,
       teacher_name, teacher_title, teacher_id, credit, hours_total, hours_week,
       weeks, weekday, periods, campus, schedule_text, assessment, nature,
       category, remark
FROM courses WHERE id = ? ORDER BY term DESC LIMIT 1`, id)
	course, err := scanCourse(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Course{}, ErrNotFound
	}
	if err != nil {
		return Course{}, fmt.Errorf("get course: %w", err)
	}
	if err := s.attachHuoshui(ctx, &course); err != nil {
		return Course{}, err
	}
	return course, nil
}

// CourseGroup aggregates every teaching class of one course (course_code)
// across terms: 同一门课不同老师、不同学期的班都展示在一个页面上。
type CourseGroup struct {
	CourseCode string   `json:"course_code"`
	CourseName string   `json:"course_name"`
	College    string   `json:"college,omitempty"`
	Credit     float64  `json:"credit,omitempty"`
	Classes    []Course `json:"classes"`
}

// ListCourseClassesByCode returns all teaching classes of a course code,
// newest term first.
func (s *Store) ListCourseClassesByCode(ctx context.Context, code string) (CourseGroup, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return CourseGroup{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, term, college, course_code, course_name, class_num,
       teacher_name, teacher_title, teacher_id, credit, hours_total, hours_week,
       weeks, weekday, periods, campus, schedule_text, assessment, nature,
       category, remark
FROM courses WHERE course_code = ?
ORDER BY term DESC, id ASC`, code)
	if err != nil {
		return CourseGroup{}, fmt.Errorf("list course classes: %w", err)
	}
	defer rows.Close()
	classes := make([]Course, 0, 4)
	for rows.Next() {
		course, err := scanCourse(rows)
		if err != nil {
			return CourseGroup{}, fmt.Errorf("scan course class: %w", err)
		}
		classes = append(classes, course)
	}
	if err := rows.Err(); err != nil {
		return CourseGroup{}, fmt.Errorf("iterate course classes: %w", err)
	}
	if len(classes) == 0 {
		return CourseGroup{}, ErrNotFound
	}
	if err := s.attachHuoshuiCourses(ctx, classes); err != nil {
		return CourseGroup{}, err
	}
	return CourseGroup{
		CourseCode: code,
		CourseName: classes[0].CourseName,
		College:    classes[0].College,
		Credit:     classes[0].Credit,
		Classes:    classes,
	}, nil
}

// ListTeacherCourses returns the teaching classes linked to a directory
// teacher, newest term first.
func (s *Store) ListTeacherCourses(ctx context.Context, teacherID string) ([]CourseSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	teacherID = strings.TrimSpace(teacherID)
	if teacherID == "" {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, term, college, course_code, course_name, class_num,
       teacher_name, teacher_id, credit, weekday, periods, campus, nature, category
FROM courses
WHERE teacher_id = ?
ORDER BY term DESC, course_name COLLATE NOCASE ASC, id ASC`, teacherID)
	if err != nil {
		return nil, fmt.Errorf("list teacher courses: %w", err)
	}
	defer rows.Close()
	data := make([]CourseSummary, 0, 8)
	for rows.Next() {
		course, err := scanCourseSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan teacher course: %w", err)
		}
		data = append(data, course)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate teacher courses: %w", err)
	}
	return data, nil
}

func scanCourseSummary(row rowScanner) (CourseSummary, error) {
	var course CourseSummary
	if err := row.Scan(
		&course.ID, &course.Term, &course.College, &course.CourseCode,
		&course.CourseName, &course.ClassNum, &course.TeacherName, &course.TeacherID,
		&course.Credit, &course.Weekday, &course.Periods, &course.Campus,
		&course.Nature, &course.Category,
	); err != nil {
		return CourseSummary{}, err
	}
	return course, nil
}

func scanCourse(row rowScanner) (Course, error) {
	var course Course
	if err := row.Scan(
		&course.ID, &course.Term, &course.College, &course.CourseCode,
		&course.CourseName, &course.ClassNum, &course.TeacherName, &course.TeacherTitle,
		&course.TeacherID, &course.Credit, &course.HoursTotal, &course.HoursWeek,
		&course.Weeks, &course.Weekday, &course.Periods, &course.Campus,
		&course.ScheduleText, &course.Assessment, &course.Nature,
		&course.Category, &course.Remark,
	); err != nil {
		return Course{}, err
	}
	return course, nil
}

type teacherRow struct {
	ID               string
	ProfileURL       string
	Name             string
	DirectoryName    string
	ProfileName      string
	Initial          string
	DirectoryNum     int
	NameMatch        bool
	Position         string
	SupervisorRoles  string
	College          string
	PrimaryRole      string
	EntryDate        string
	EmploymentStatus string
	Education        string
	Degree           string
	University       string
	Gender           string
	Email            string
	Phone            string
	OfficeLocation   string
	PostalCode       string
	Introduction     string
	ResearchJSON     string
	AvatarURL        string
	SectionsJSON     string
	Status           string
}

func buildRows(snapshot Snapshot) ([]teacherRow, error) {
	rows := make([]teacherRow, 0, len(snapshot.Teachers))
	seenURLs := make(map[string]struct{}, len(snapshot.Teachers))
	for index, teacher := range snapshot.Teachers {
		if strings.TrimSpace(teacher.Status) != "" && strings.TrimSpace(teacher.Status) != "ok" {
			continue
		}
		profileURL := strings.TrimSpace(teacher.ProfileURL)
		name := sanitizePublicText(teacher.Name)
		if name == "" || profileURL == "" {
			return nil, fmt.Errorf("teacher at index %d is missing name or profile_url", index)
		}
		parsedURL, err := url.Parse(profileURL)
		if err != nil || parsedURL.Hostname() == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return nil, fmt.Errorf("teacher %q has invalid profile_url", name)
		}
		if _, exists := seenURLs[profileURL]; exists {
			return nil, fmt.Errorf("duplicate profile_url %q", profileURL)
		}
		seenURLs[profileURL] = struct{}{}
		research := make([]string, 0, len(teacher.Research))
		for _, direction := range teacher.Research {
			if direction = sanitizePublicText(direction); direction != "" {
				research = append(research, direction)
			}
		}
		researchJSON, err := json.Marshal(research)
		if err != nil {
			return nil, fmt.Errorf("encode research directions for %q: %w", name, err)
		}
		sections, err := sanitizeSections(teacher.Sections)
		if err != nil {
			return nil, fmt.Errorf("encode profile sections for %q: %w", name, err)
		}
		sectionsJSON, err := json.Marshal(sections)
		if err != nil {
			return nil, fmt.Errorf("encode profile sections for %q: %w", name, err)
		}
		status := strings.TrimSpace(teacher.Status)
		if status == "" {
			status = "ok"
		}
		rows = append(rows, teacherRow{
			ID:               teacherID(profileURL),
			ProfileURL:       profileURL,
			Name:             name,
			DirectoryName:    sanitizePublicText(teacher.DirectoryName),
			ProfileName:      sanitizePublicText(teacher.ProfileName),
			Initial:          strings.ToLower(strings.TrimSpace(teacher.Initial)),
			DirectoryNum:     teacher.DirectoryNum,
			NameMatch:        teacher.NameMatch,
			Position:         sanitizePublicText(teacher.Position),
			SupervisorRoles:  sanitizePublicText(teacher.SupervisorRoles),
			College:          sanitizePublicText(teacher.College),
			PrimaryRole:      sanitizePublicText(teacher.PrimaryRole),
			EntryDate:        sanitizePublicText(teacher.EntryDate),
			EmploymentStatus: sanitizePublicText(teacher.EmploymentStatus),
			Education:        sanitizePublicText(teacher.Education),
			Degree:           sanitizePublicText(teacher.Degree),
			University:       sanitizePublicText(teacher.University),
			Gender:           sanitizePublicText(teacher.Gender),
			Email:            sanitizeContactValue(teacher.Email),
			Phone:            sanitizeContactValue(teacher.Phone),
			OfficeLocation:   sanitizeContactValue(teacher.OfficeLocation),
			PostalCode:       sanitizeContactValue(teacher.PostalCode),
			Introduction:     sanitizePublicText(teacher.Introduction),
			ResearchJSON:     string(researchJSON),
			AvatarURL:        sanitizeAvatarURL(teacher.AvatarURL, profileURL),
			SectionsJSON:     string(sectionsJSON),
			Status:           status,
		})
	}
	return rows, nil
}

func sanitizeSections(input []SnapshotSection) ([]TeacherSection, error) {
	sections := make([]TeacherSection, 0, len(input))
	seen := make(map[string]bool)
	for _, section := range input {
		title := sanitizePublicText(section.Title)
		if title == "" || isContactSectionTitle(title) || seen[title] {
			continue
		}
		content := sanitizePublicText(section.Content)
		if content == "" {
			continue
		}
		seen[title] = true
		sections = append(sections, TeacherSection{Title: title, Content: content})
	}
	return sections, nil
}

func isContactSectionTitle(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{"联系方式", "联系信息", "通讯/办公地址", "办公地点", "办公地址", "contact", "email", "telephone", "phone"} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func sanitizeContactValue(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if encryptedContactPattern.MatchString(value) {
		return ""
	}
	if len([]rune(value)) > 500 {
		value = string([]rune(value)[:500])
	}
	return value
}

func sanitizeAvatarURL(raw, profileURL string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	base, err := url.Parse(profileURL)
	if err != nil || base.Hostname() == "" {
		return ""
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	if (resolved.Scheme != "http" && resolved.Scheme != "https") || !strings.EqualFold(resolved.Hostname(), base.Hostname()) {
		return ""
	}
	resolved.Fragment = ""
	return resolved.String()
}

func teacherID(profileURL string) string {
	digest := sha256.Sum256([]byte(profileURL))
	return hex.EncodeToString(digest[:12])
}

func (s *Store) ListTeachers(ctx context.Context, filter TeacherFilter) (TeacherPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	page := filter.Page
	if page < defaultPage {
		page = defaultPage
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	search := sanitizeQuery(filter.Search)
	initial := strings.ToLower(strings.TrimSpace(filter.Initial))
	college := sanitizeQuery(filter.College)
	where, args := teacherWhere(search, initial, college)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM teachers WHERE `+where, args...).Scan(&total); err != nil {
		return TeacherPage{}, fmt.Errorf("count teachers: %w", err)
	}
	offset := (page - 1) * pageSize
	rows, err := s.db.QueryContext(ctx, `
SELECT id, profile_url, name, initial, directory_num, position,
       supervisor_roles, college, primary_role, employment_status,
       education, degree, university, introduction, research_directions_json, avatar_url
FROM teachers
WHERE `+where+`
ORDER BY name COLLATE NOCASE ASC, id ASC
LIMIT ? OFFSET ?`, append(args, pageSize, offset)...)
	if err != nil {
		return TeacherPage{}, fmt.Errorf("list teachers: %w", err)
	}
	defer rows.Close()
	data := make([]TeacherSummary, 0, pageSize)
	for rows.Next() {
		teacher, err := scanTeacherSummary(rows)
		if err != nil {
			return TeacherPage{}, fmt.Errorf("scan teacher: %w", err)
		}
		data = append(data, teacher)
	}
	if err := rows.Err(); err != nil {
		return TeacherPage{}, fmt.Errorf("iterate teachers: %w", err)
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return TeacherPage{
		Data: data,
		Pagination: Pagination{
			Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages,
		},
	}, nil
}

func teacherWhere(search, initial, college string) (string, []any) {
	conditions := []string{"1 = 1"}
	args := make([]any, 0, 5)
	if search != "" {
		pattern := likePattern(search)
		conditions = append(conditions, `(name LIKE ? ESCAPE '\' OR directory_name LIKE ? ESCAPE '\' OR college LIKE ? ESCAPE '\' OR position LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	if initial != "" {
		conditions = append(conditions, `initial = ?`)
		args = append(args, initial)
	}
	if college != "" {
		conditions = append(conditions, `college LIKE ? ESCAPE '\'`)
		args = append(args, likePattern(college))
	}
	return strings.Join(conditions, " AND "), args
}

func (s *Store) GetTeacher(ctx context.Context, id string) (Teacher, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Teacher{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, profile_url, name, initial, directory_num, position,
       supervisor_roles, college, primary_role, entry_date, employment_status,
       education, degree, university, gender, email, phone, office_location,
       postal_code, introduction, research_directions_json, avatar_url, sections_json
FROM teachers WHERE id = ?`, id)
	teacher, err := scanTeacher(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Teacher{}, ErrNotFound
	}
	if err != nil {
		return Teacher{}, fmt.Errorf("get teacher: %w", err)
	}
	return teacher, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTeacher(row rowScanner) (Teacher, error) {
	var teacher Teacher
	var researchJSON string
	var sectionsJSON string
	if err := row.Scan(
		&teacher.ID, &teacher.ProfileURL, &teacher.Name, &teacher.Initial,
		&teacher.DirectoryNum, &teacher.Position, &teacher.SupervisorRoles,
		&teacher.College, &teacher.PrimaryRole, &teacher.EntryDate, &teacher.EmploymentStatus,
		&teacher.Education, &teacher.Degree, &teacher.University, &teacher.Gender,
		&teacher.Email, &teacher.Phone, &teacher.OfficeLocation,
		&teacher.PostalCode, &teacher.Introduction, &researchJSON, &teacher.AvatarURL, &sectionsJSON,
	); err != nil {
		return Teacher{}, err
	}
	if err := json.Unmarshal([]byte(researchJSON), &teacher.Research); err != nil {
		return Teacher{}, fmt.Errorf("decode research directions: %w", err)
	}
	if teacher.Research == nil {
		teacher.Research = []string{}
	}
	if strings.TrimSpace(sectionsJSON) == "" {
		sectionsJSON = "[]"
	}
	if err := json.Unmarshal([]byte(sectionsJSON), &teacher.Sections); err != nil {
		return Teacher{}, fmt.Errorf("decode profile sections: %w", err)
	}
	if teacher.Sections == nil {
		teacher.Sections = []TeacherSection{}
	}
	return teacher, nil
}

func scanTeacherSummary(row rowScanner) (TeacherSummary, error) {
	var teacher TeacherSummary
	var researchJSON string
	if err := row.Scan(
		&teacher.ID, &teacher.ProfileURL, &teacher.Name, &teacher.Initial,
		&teacher.DirectoryNum, &teacher.Position, &teacher.SupervisorRoles,
		&teacher.College, &teacher.PrimaryRole, &teacher.EmploymentStatus,
		&teacher.Education, &teacher.Degree, &teacher.University,
		&teacher.Introduction, &researchJSON, &teacher.AvatarURL,
	); err != nil {
		return TeacherSummary{}, err
	}
	if strings.TrimSpace(researchJSON) == "" {
		researchJSON = "[]"
	}
	if err := json.Unmarshal([]byte(researchJSON), &teacher.Research); err != nil {
		return TeacherSummary{}, fmt.Errorf("decode research directions: %w", err)
	}
	if teacher.Research == nil {
		teacher.Research = []string{}
	}
	return teacher, nil
}

func sanitizeQuery(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	value = strings.ReplaceAll(value, "_", `\_`)
	return "%" + value + "%"
}

func sanitizePublicText(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	lower := strings.ToLower(value)
	end := len(value)
	for _, marker := range []string{
		"邮箱", "电子邮件", "email", "e-mail", "emial", "联系方式",
		"other contact information", "通讯/办公地址", "办公地点", "邮编",
		"联系电话", "电话", "telephone",
	} {
		if index := strings.Index(lower, strings.ToLower(marker)); index >= 0 && index < end {
			end = index
		}
	}
	value = strings.TrimSpace(value[:end])
	value = emailPattern.ReplaceAllString(value, "")
	value = hashEmailPattern.ReplaceAllString(value, "")
	value = phonePattern.ReplaceAllString(value, "")
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
