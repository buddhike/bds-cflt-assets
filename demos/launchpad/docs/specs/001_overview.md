# Launchpad

- Launchpad is a system for launching various demos
- Demo is an application used to demonstrate the product functionality
- Launchpad manages the lifecycle of a demo
- Demo lifecycle is launch -> running -> teardown
- When you launch a demo, it will deploy it to the AWS account
- All demos have a cloudformation template associated with it
- Launchpad runs with permissions to create cloudformation resources
- Cloudformation template exposed to launchpad should have a standard interface so that they can be launced in a consistent manner
- Launchpad must have simple user store
- Users can register with their @confluent.io
- When signing up, user received an OTP to the email address they enter
- Once the OTP is validated, if the email address is bdesilva@confluent.io (this is configurable when deploying Launchpad); the user gets access as super user. Otherwise user is placed in a review list
- Super user can review a list of access requests and approve them or deny them
- Once logged into the Launchpad, list of demos will be presented
- Demos can be launched from the list
- If a demo is already running, you can access the URL to the dashboard of that demo
- Launchpad should be built only on top of serverless services in AWS. Use AWS Lambda for compute, SES for email and DynamoDB for storage
- Demos are discovered from the git repostory (github.com/buddhike/bds-cflt-assets). Each demo is in a sub directory under launchpad-demos directory. They always contain a tf directory with terraform templates for that demo.
- When launched, launchpad should run the terraform package to deploy the demo 
- Terraform state must be saved in an S3 bucket. Use a path specific to each demo so that the state files are not overriden when setting up multiple demos
- Launchpad itself should have a simple cloudformation template to deploy it
- Launchpad resources and the demos launched by launchpad must always have a tag so that they can be identified easily
- Launchapd also provide each user a place to safely store their confluent cloud credentials to be used when creating resources via terraform 
- Launchpad frontend is built with React
- Launcpad lambda functions are written in Python
- Launchpad itself has a Cloudwatch dashboard to monitor its operations
- Demos should have a standard way to expose a URL entrypoint to demo. Launchpad then reads it and makes it available to the user to navigate into it.

