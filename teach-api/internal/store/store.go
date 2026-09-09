// Package store owns the SQLite representation of the public teaching
// directory. It deliberately has no model for private grades or account data.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	Huoshui     *HuoshuiRef `json:"huoshui"`
}

type CourseFilter struct {
	Page     int
	PageSize int
	Search   string
	Campus   string
	Weekday  string
	College  string
}

type CoursePage struct {
	Data       []CourseSummary `json:"data"`
	Pagination Pagination      `json:"pagination"`
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
	id TEXT PRIMARY KEY NOT NULL,
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
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS courses_teacher_id_idx ON courses(teacher_id);
CREATE INDEX IF NOT EXISTS courses_name_idx ON courses(course_name COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS courses_campus_idx ON courses(campus);
CREATE INDEX IF NOT EXISTS courses_weekday_idx ON courses(weekday);
CREATE INDEX IF NOT EXISTS courses_college_idx ON courses(college);
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
	registrar_course_id TEXT PRIMARY KEY NOT NULL,
	huoshui_course_id TEXT NOT NULL REFERENCES huoshui_courses(object_id)
);
CREATE INDEX IF NOT EXISTS huoshui_match_huoshui_idx ON huoshui_match(huoshui_course_id);`)
	if err != nil {
		return fmt.Errorf("create sqlite schema: %w", err)
	}
	if err := s.ensureTeacherColumns(); err != nil {
		return err
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM courses`); err != nil {
		rollback()
		return fmt.Errorf("replace courses: %w", err)
	}
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO courses (
	id, term, college, course_code, course_name, class_num,
	teacher_name, teacher_title, teacher_id, credit, hours_total, hours_week,
	weeks, weekday, periods, campus, schedule_text, assessment, nature,
	category, remark, updated_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
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
			sanitizePublicText(course.Nature), sanitizePublicText(course.Category), sanitizePublicText(course.Remark),
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
	if err := s.loadMeta(ctx); err != nil {
		return fmt.Errorf("reload metadata: %w", err)
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

func (s *Store) ListCourses(ctx context.Context, filter CourseFilter) (CoursePage, error) {
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
	)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM courses WHERE `+where, args...).Scan(&total); err != nil {
		return CoursePage{}, fmt.Errorf("count courses: %w", err)
	}
	offset := (page - 1) * pageSize
	rows, err := s.db.QueryContext(ctx, `
SELECT id, term, college, course_code, course_name, class_num,
       teacher_name, teacher_id, credit, weekday, periods, campus
FROM courses
WHERE `+where+`
ORDER BY course_name COLLATE NOCASE ASC, id ASC
LIMIT ? OFFSET ?`, append(args, pageSize, offset)...)
	if err != nil {
		return CoursePage{}, fmt.Errorf("list courses: %w", err)
	}
	defer rows.Close()
	data := make([]CourseSummary, 0, pageSize)
	for rows.Next() {
		course, err := scanCourseSummary(rows)
		if err != nil {
			return CoursePage{}, fmt.Errorf("scan course: %w", err)
		}
		data = append(data, course)
	}
	if err := rows.Err(); err != nil {
		return CoursePage{}, fmt.Errorf("iterate courses: %w", err)
	}
	if err := s.attachHuoshuiSummaries(ctx, data); err != nil {
		return CoursePage{}, err
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return CoursePage{
		Data: data,
		Pagination: Pagination{
			Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages,
		},
	}, nil
}

func courseWhere(search, campus, weekday, college string) (string, []any) {
	conditions := []string{"1 = 1"}
	args := make([]any, 0, 7)
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
	return strings.Join(conditions, " AND "), args
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
FROM courses WHERE id = ?`, id)
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
       teacher_name, teacher_id, credit, weekday, periods, campus
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
