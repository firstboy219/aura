# ADR-0053 — App accounts: username/password sign-in for the Android client

Status: **Proposed** (2026-11-10). Owner request: when signing in from the APK, the token is generated automatically. The
owner asked for an account `admin` for the app.

## Decision
- **Accounts.** The owner creates app accounts with `aura admin app-account set <user> <role> --stdin`; the password goes
  on stdin, never in the process list.
  - Passwords are hashed with argon2id (the same `webauth` code as the web owner login) and must be at least 10 characters.
  - The role is viewer, operator or approver. **Never owner:** credentials, tools, workflows, Lab approvals and admin stay
    with the owner's own credentials.
- **Sign-in.** `POST /api/v1/app/login {username, password, device}` is unauthenticated.
  - It is rate limited (10 per minute per address), and 5 failures lock the account for 15 minutes.
  - An unknown user and a wrong password give the same answer and take the same work (dummy argon2 verify).
  - Success issues a **device token**: a member row linked to the account, storing only the token's hash, named
    `app:<user>:<device>:<id>`. Every sign-in, failure and lock is audited.
- **Sign-out and revocation.**
  - `POST /api/v1/app/logout` revokes the calling device.
  - Changing an account's password or role signs out all its devices.
  - Disabling an account (`aura admin app-account disable`) does the same, and so does disabling a single member.
- **App (0.2.0).** The settings screen asks for a username and password. The app keeps only the device token (encrypted
  with an Android Keystore key), never the password.

## Account created
`admin`, role **approver**: it can create tasks, answer questions and approve or reject actions. The password was set as
the owner gave it. It meets the minimum length but is a dictionary-style word plus digits. The lockout and rate limit make
online guessing slow, but a stronger password is recommended.

## Evidence
- `app/app_accounts_test.go`:
  - short password and owner role refused;
  - sign-in returns an approver device token, which is stored hashed only;
  - the device token cannot reach owner admin;
  - logout revokes it;
  - an unknown user looks the same as a wrong password;
  - 5 failures lock the account;
  - a password change unlocks the account and signs out every device.
- Production check: sign-in → `GET /tasks` 200 → logout → 401.
