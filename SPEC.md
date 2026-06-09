

# UI
- dark mode only
- black main color
- white secondary color
- green accent color

# Login Page
- login page
- username input
- password input

# Admin User
- only user for system
- username and password set via ENV variables
- cli make settings VARS easy

# Database
- sqlite only
- single database

# Login Banning System
- many bad logins cause ip ban
- system auto-purge old records
- db no grow forever
- cli make clearing bans easy

# Easy Dep Installation
- cli make deps installation easy
- user no need to think about deps installation
- cli make easy for linux, mac, and wsl
- no need deep windows support

# Built Using Rust
- use axum framework
- build using rust

# Makefile
- make install should install the cli

# Cardholders
- admin create cardholders by name
- admin send secret link to cardholders
- use secret link to upload receipts
- secret link managed by ENV variable
- VARS easy to change using cli

# Stores
- admin create stores
- stores can be associated with receipts

# Receipt Upload Form
- form have option list for names
- cardholder select name from dropdown
- cardholder provide one or many images per upload
- one upload equals one expense
- form have price field
- place of purchase field
- checkbox to select one or many stores
- description field
- notes field
- cardholder fill out form to submit receipts
- images get converted to pdf
- all pdfs get merged into a single output pdf
- one receipt upload equals one final pdf
- final pdf contain all images for expenses
- images usually taken with phone camera
- no want super giant images
- big enough to read reasonably, but no giant
- app care about space, upload speed, and download speed
- big images result in big slowdowns
- maybe client-side do the shrinking/scaling
- then smaller image for server to manage
- easier on everybody

# Managing Receipts
- admin have access to all receipts and details
- admin come to portal to download receipts
- admin delete receipts if needed to save space
- total space used by app shown in admin portal
- admin care about space

# Dockerize
- application should have Dockerfile
- Dockerfile no manage deps installation
- Dockerfile only install rust and receipt-upload cli
- receipt-upload cli then manage deps installtion
- deps easy to install using cli
- make deployment easier

# Speed of Upload/Download
- already mention upload / download speed important
- must be quick for impatient user
- but no be super complicated

# Multiple Images with Phone
- cardholder may take many images with phone
- im past, have issue with new phone taken images overriding old ones
- need to be sure many images can be taken with device
- super useful for cardholder

# Deps in Use
- axum for HTTP routing, forms, multipart uploads
- tokio for async runtime
- rusqlite with bundled SQLite for single-file database
- clap for receipt-upload CLI
- serde and serde_json for persistent config file
- hmac, sha2, base64, subtle for signed admin session cookie
- time for timestamps and login ban windows
- image for image decoding, resizing, and JPEG compression
- uuid for unique PDF filenames
- rand and rpassword for secret generation and password prompt
- html-escape for server-rendered HTML escaping
- dirs for platform config directory discovery
- anyhow for simple application error handling
- no external image conversion runtime dependency; Rust handles resize and PDF generation

# Spec Clarifications Added During Migration
- stores are many-to-many with receipts using checkboxes and a receipt_stores table
- upload accepts receipt images only, not PDFs, because the Rust speed path resizes images before creating the final PDF
- each image is resized to max 1600px on the longest side and JPEG-compressed before PDF generation
- browser also attempts client-side resizing before upload to reduce phone-camera upload size
- external image conversion install setting removed because no external image conversion dependency remains

# App Work Good outside Docker too
- should work nicely anywhere
- but not be super complex
- simple is good
