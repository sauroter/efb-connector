#!/usr/bin/env python3
"""
Garmin Connect GPX fetcher for water sport activities.

Usage:
    python garmin_fetch.py list [--days N]
    python garmin_fetch.py fetch <activity_id> [--output DIR]
    python garmin_fetch.py fetch-all [--days N] [--output DIR]

Environment variables:
    GARMIN_EMAIL - Garmin Connect email
    GARMIN_PASSWORD - Garmin Connect password

Or use 1Password references in config.json.
"""

import argparse
import json
import os
import sys
from datetime import datetime, timedelta
from pathlib import Path

try:
    from garminconnect import Garmin
except ImportError:
    print("Error: garminconnect not installed. Run: pip install garminconnect", file=sys.stderr)
    sys.exit(1)

# Water sport activity types to filter
WATER_SPORT_TYPES = [
    "kayaking",
    "whitewater_rafting_v2",
    "stand_up_paddleboarding",
    "paddling",
    "canoeing",
    "rowing",
]


def load_config():
    """Load config from config.json if it exists."""
    config_paths = [
        Path("config.json"),
        Path.home() / ".config" / "efb-connector" / "config.json",
    ]

    for path in config_paths:
        if path.exists():
            with open(path) as f:
                return json.load(f)
    return {}


def get_credentials(config):
    """Get Garmin credentials from config, 1Password, or environment."""
    garmin_config = config.get("garmin", {})

    # Try 1Password first if configured
    op_config = garmin_config.get("onepassword", {})
    if op_config.get("account") and op_config.get("item"):
        import subprocess

        vault = op_config.get("vault", "Private")
        item = op_config["item"]
        account = op_config["account"]
        email_field = op_config.get("email_field", "username")
        password_field = op_config.get("password_field", "password")

        try:
            email_ref = f"op://{vault}/{item}/{email_field}"
            password_ref = f"op://{vault}/{item}/{password_field}"

            email = subprocess.check_output(
                ["op", "read", email_ref, "--account", account],
                text=True
            ).strip()

            password = subprocess.check_output(
                ["op", "read", password_ref, "--account", account],
                text=True
            ).strip()

            if email and password:
                return email, password
        except (subprocess.CalledProcessError, FileNotFoundError):
            pass

    # Fall back to environment variables
    email = os.environ.get("GARMIN_EMAIL", "")
    password = os.environ.get("GARMIN_PASSWORD", "")

    if not email or not password:
        print("Error: Garmin credentials not found.", file=sys.stderr)
        print("Set GARMIN_EMAIL and GARMIN_PASSWORD environment variables,", file=sys.stderr)
        print("or configure 1Password in config.json", file=sys.stderr)
        sys.exit(1)

    return email, password


def connect_garmin(config):
    """Connect to Garmin and return client."""
    email, password = get_credentials(config)

    try:
        client = Garmin(email, password)
        client.login()
        return client
    except Exception as e:
        print(f"Error connecting to Garmin: {e}", file=sys.stderr)
        sys.exit(1)


def list_activities(client, days=30):
    """List water sport activities from the last N days."""
    start_date = datetime.now() - timedelta(days=days)

    activities = client.get_activities_by_date(
        start_date.strftime("%Y-%m-%d"),
        datetime.now().strftime("%Y-%m-%d")
    )

    water_activities = []
    for activity in activities:
        activity_type = activity.get("activityType", {}).get("typeKey", "")
        if activity_type in WATER_SPORT_TYPES:
            water_activities.append({
                "id": activity["activityId"],
                "name": activity.get("activityName", "Unnamed"),
                "type": activity_type,
                "date": activity.get("startTimeLocal", "")[:10],
                "duration": activity.get("duration", 0),
                "distance": activity.get("distance", 0),
            })

    return water_activities


def fetch_gpx(client, activity_id, output_dir="."):
    """Fetch GPX file for an activity."""
    output_path = Path(output_dir)
    output_path.mkdir(parents=True, exist_ok=True)

    try:
        gpx_data = client.download_activity(activity_id, dl_fmt=client.ActivityDownloadFormat.GPX)

        filename = f"activity_{activity_id}.gpx"
        filepath = output_path / filename

        with open(filepath, "wb") as f:
            f.write(gpx_data)

        return str(filepath)
    except Exception as e:
        print(f"Error fetching GPX for activity {activity_id}: {e}", file=sys.stderr)
        return None


def main():
    parser = argparse.ArgumentParser(description="Fetch GPX files from Garmin Connect")
    subparsers = parser.add_subparsers(dest="command", help="Commands")

    # List command
    list_parser = subparsers.add_parser("list", help="List water sport activities")
    list_parser.add_argument("--days", type=int, default=30, help="Number of days to look back (default: 30)")
    list_parser.add_argument("--json", action="store_true", help="Output as JSON")

    # Fetch command
    fetch_parser = subparsers.add_parser("fetch", help="Fetch GPX for a specific activity")
    fetch_parser.add_argument("activity_id", type=int, help="Activity ID to fetch")
    fetch_parser.add_argument("--output", "-o", default=".", help="Output directory (default: current)")

    # Fetch-all command
    fetch_all_parser = subparsers.add_parser("fetch-all", help="Fetch GPX for all water sport activities")
    fetch_all_parser.add_argument("--days", type=int, default=30, help="Number of days to look back (default: 30)")
    fetch_all_parser.add_argument("--output", "-o", default=".", help="Output directory (default: current)")
    fetch_all_parser.add_argument("--json", action="store_true", help="Output results as JSON")

    args = parser.parse_args()

    if not args.command:
        parser.print_help()
        sys.exit(1)

    config = load_config()
    client = connect_garmin(config)

    if args.command == "list":
        activities = list_activities(client, args.days)

        if args.json:
            print(json.dumps(activities))
        else:
            if not activities:
                print(f"No water sport activities found in the last {args.days} days.")
            else:
                print(f"Water sport activities (last {args.days} days):")
                print("-" * 60)
                for act in activities:
                    duration_min = int(act["duration"] / 60) if act["duration"] else 0
                    distance_km = act["distance"] / 1000 if act["distance"] else 0
                    print(f"  {act['id']}: {act['date']} - {act['name']}")
                    print(f"           Type: {act['type']}, {duration_min} min, {distance_km:.1f} km")

    elif args.command == "fetch":
        filepath = fetch_gpx(client, args.activity_id, args.output)
        if filepath:
            print(filepath)
        else:
            sys.exit(1)

    elif args.command == "fetch-all":
        activities = list_activities(client, args.days)

        if not activities:
            if args.json:
                print(json.dumps([]))
            else:
                print(f"No water sport activities found in the last {args.days} days.")
            return

        results = []
        for act in activities:
            filepath = fetch_gpx(client, act["id"], args.output)
            if filepath:
                results.append({
                    "id": act["id"],
                    "name": act["name"],
                    "date": act["date"],
                    "file": filepath
                })
                if not args.json:
                    print(f"Downloaded: {filepath}")

        if args.json:
            print(json.dumps(results))


if __name__ == "__main__":
    main()
