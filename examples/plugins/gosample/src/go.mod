module gosample

go 1.25

require github.com/orkcom-tech/cogitorium/sdk/go v0.0.0

// While the SDK ships in this repository rather than from a tag. A plugin of
// your own drops this line and takes the published module.
replace github.com/orkcom-tech/cogitorium/sdk/go => ../../../../sdk/go
