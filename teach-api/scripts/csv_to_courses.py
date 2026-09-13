#!/usr/bin/env python3
"""把教务导出的课程 CSV 转换成 teach-api 的课程快照 JSON。

支持两种教务导出格式(自动按表头识别):

1. 选修/必修课程清单(选修其他课程_共4623条.csv):
   序号,课程代码,课程名称,课程类别,课程类型,学分,教师,职称,其他教师,上课时间地点,校区,开课学院,教学班号(teachId)
2. 体育课程数据(体育课程数据_307条.csv):
   一门课(teachId)对应多个项目(projId),合成 ID 为 "<teachId>-<n>"(按项目名排序),
   课程名写作 "体育Ⅲ(篮球)",授课教师取项目教师(staffName)。
3. 全量课程清单(swjtu_all_courses.csv,按表头含"选课编号"自动识别):
   序号,选课编号,课程代码,课程名称,班号,学分,性质,院系,教师,职称,时间地点,优选班级,状态,申请人数,校区,限制
   该格式单文件转换,必须显式给学期(通常是归档学期)。

可选第三个参数是旧版课程快照(courses.json):其中未被新数据覆盖的记录会保留
(新 CSV 里同教学班号的记录优先)。

用法:
    python3 csv_to_courses.py 选修其他课程.csv 体育课程数据.csv 输出.json [旧快照.json]
    python3 csv_to_courses.py 全量课程.csv 输出.json 学期
"""

import csv
import json
import re
import sys
from datetime import datetime, timezone

TERM = "2026-2027第1学期"
SCHEDULE_PATTERN = re.compile(r"([0-9,\-]+周)\s*(星期[一二三四五六日])\s*([0-9,\-]+节)")


def clean(value):
    if value is None:
        return ""
    return " ".join(str(value).split())


def parse_schedule(text):
    """从 "2-17周 星期三 3-5节;J4307(九里)" 里抽出第一个时段,全文保留在 schedule_text。"""
    match = SCHEDULE_PATTERN.search(text)
    if not match:
        return "", "", ""
    weeks = match.group(1).removesuffix("周")
    periods = match.group(3).removesuffix("节")
    return weeks, match.group(2), periods


def convert_general(reader):
    courses = []
    for row in reader:
        teach_id = clean(row.get("教学班号(teachId)"))
        if not teach_id:
            continue
        teacher = clean(row.get("教师")) or clean(row.get("其他教师")) or "待定"
        schedule = clean(row.get("上课时间地点"))
        weeks, weekday, periods = parse_schedule(schedule)
        credit = clean(row.get("学分"))
        try:
            number = float(credit)
            credit = int(number) if number == int(number) else number
        except ValueError:
            credit = 0
        courses.append({
            "id": teach_id,
            "term": TERM,
            "college": clean(row.get("开课学院")),
            "course_code": clean(row.get("课程代码")),
            "course_name": clean(row.get("课程名称")),
            "teacher_name": teacher,
            "teacher_title": clean(row.get("职称")),
            "credit": credit,
            "weeks": weeks,
            "weekday": weekday,
            "periods": periods,
            "campus": clean(row.get("校区")),
            "schedule_text": schedule,
            "nature": clean(row.get("课程类型")),
            "category": clean(row.get("课程类别")),
            "remark": "",
        })
    return courses


def convert_pe(reader):
    """体育课程:teachId 是课程层,projId/projName 是项目层,一层一层展开成教学班。"""
    groups = {}
    for row in reader:
        teach_id = clean(row.get("teachId"))
        if not teach_id:
            continue
        groups.setdefault(teach_id, []).append(row)
    courses = []
    for teach_id in sorted(groups):
        rows = sorted(groups[teach_id], key=lambda r: (clean(r.get("projName")), clean(r.get("projId"))))
        for index, row in enumerate(rows, 1):
            project = clean(row.get("projName"))
            name = clean(row.get("courseName"))
            if project:
                name = f"{name}({project})"
            time_text = clean(row.get("courseClassTime")) or clean(row.get("classTime"))
            location = clean(row.get("classLocation")) or clean(row.get("courseClassLocation"))
            schedule = time_text
            if location:
                schedule = f"{time_text};{location}" if time_text else location
            weeks, weekday, periods = parse_schedule(time_text)
            lead = clean(row.get("courseStaffName"))
            teacher = clean(row.get("staffName")) or lead or "待定"
            remark = f"课程负责人:{lead}" if lead and lead != teacher else ""
            courses.append({
                "id": f"{teach_id}-{index}",
                "term": TERM,
                "college": "体育学院",
                "course_code": clean(row.get("courseCode")),
                "course_name": name,
                "teacher_name": teacher,
                "teacher_title": "",
                "credit": 0,
                "weeks": weeks,
                "weekday": weekday,
                "periods": periods,
                "campus": clean(row.get("campusName")),
                "schedule_text": schedule,
                "nature": "",
                "category": "体育课程",
                "remark": remark,
            })
    return courses


def convert_all(reader, term):
    """全量课程清单:选课编号即教学班 ID,一行一个教学班。课程名称为空的行跳过。"""
    courses = []
    skipped = 0
    for row in reader:
        teach_id = clean(row.get("选课编号"))
        name = clean(row.get("课程名称"))
        if not teach_id:
            continue
        if not name:
            skipped += 1
            continue
        schedule = clean(row.get("时间地点"))
        weeks, weekday, periods = parse_schedule(schedule)
        credit = clean(row.get("学分"))
        try:
            number = float(credit)
            credit = int(number) if number == int(number) else number
        except ValueError:
            credit = 0
        preferred = clean(row.get("优选班级"))
        courses.append({
            "id": teach_id,
            "term": term,
            "college": clean(row.get("院系")),
            "course_code": clean(row.get("课程代码")),
            "course_name": name,
            "class_num": clean(row.get("班号")),
            "teacher_name": clean(row.get("教师")) or "待定",
            "teacher_title": clean(row.get("职称")),
            "credit": credit,
            "weeks": weeks,
            "weekday": weekday,
            "periods": periods,
            "campus": clean(row.get("校区")),
            "schedule_text": schedule,
            "nature": clean(row.get("性质")),
            "category": "",
            "remark": f"优选班级:{preferred}" if preferred else "",
        })
    if skipped:
        print(f"skipped {skipped} rows with empty 课程名称", file=sys.stderr)
    return courses


def convert_all_courses(source_path, target, term):
    with open(source_path, encoding="utf-8-sig", newline="") as source:
        courses = convert_all(csv.DictReader(source), term)
    if not courses:
        raise SystemExit("没有解析到任何课程记录")
    snapshot = {
        "schema_version": 1,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "term": term,
        "courses": sorted(courses, key=lambda course: course["id"]),
    }
    with open(target, "w", encoding="utf-8") as output:
        json.dump(snapshot, output, ensure_ascii=False, indent=1)
    print(f"converted {len(courses)} courses -> {target} (term={term})")


def convert(general_path, pe_path, target, legacy_path=None):
    courses = {}
    with open(general_path, encoding="utf-8-sig", newline="") as source:
        for course in convert_general(csv.DictReader(source)):
            courses[course["id"]] = course
    general_count = len(courses)
    with open(pe_path, encoding="utf-8-sig", newline="") as source:
        pe_courses = convert_pe(csv.DictReader(source))
    pe_count = len(pe_courses)
    for course in pe_courses:
        courses[course["id"]] = course
    legacy_count = 0
    if legacy_path:
        with open(legacy_path, encoding="utf-8") as source:
            legacy = json.load(source)
        for course in legacy.get("courses", []):
            if course.get("id") and course["id"] not in courses:
                courses[course["id"]] = course
                legacy_count += 1
    if not courses:
        raise SystemExit("没有解析到任何课程记录")
    snapshot = {
        "schema_version": 1,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "term": TERM,
        "courses": [courses[key] for key in sorted(courses)],
    }
    with open(target, "w", encoding="utf-8") as output:
        json.dump(snapshot, output, ensure_ascii=False, indent=1)
    print(f"converted {len(courses)} courses -> {target} "
          f"(general={general_count}, pe={pe_count}, legacy-kept={legacy_count}, term={TERM})")


if __name__ == "__main__":
    if len(sys.argv) >= 2:
        with open(sys.argv[1], encoding="utf-8-sig", newline="") as probe:
            all_courses_format = "选课编号" in probe.readline()
    else:
        all_courses_format = False
    if all_courses_format:
        if len(sys.argv) != 4:
            raise SystemExit("用法: python3 csv_to_courses.py 全量课程.csv 输出.json 学期")
        convert_all_courses(sys.argv[1], sys.argv[2], sys.argv[3])
    else:
        if len(sys.argv) not in (4, 5):
            raise SystemExit("用法: python3 csv_to_courses.py 选修课程.csv 体育课程.csv 输出.json [旧快照.json]")
        convert(sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4] if len(sys.argv) == 5 else None)
