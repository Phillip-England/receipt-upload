

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
- see how this section does not list deps?
- mister LLM, it is your job to replace this section with the deps you chose to include in this project
- please list them here in similar format to rest of doc

# App Work Good outside Docker too
- should work nicely anywhere
- but not be super complex
- simple is good