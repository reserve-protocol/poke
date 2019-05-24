package main

func init() {
	// The HID library is released under a different license on Linux.
	hidLicense = lgplV3
	dependencyLicenses = append(dependencyLicenses, licenseInfo{
		// Linux-only dependency of github.com/karalabe/hid
		"github.com/libusb/libusb",
`Copyright © 2001 Johannes Erdfelt <johannes@erdfelt.com>
Copyright © 2007-2009 Daniel Drake <dsd@gentoo.org>
Copyright © 2010-2012 Peter Stuge <peter@stuge.se>
Copyright © 2008-2016 Nathan Hjelm <hjelmn@users.sourceforge.net>
Copyright © 2009-2013 Pete Batard <pete@akeo.ie>
Copyright © 2009-2013 Ludovic Rousseau <ludovic.rousseau@gmail.com>
Copyright © 2010-2012 Michael Plante <michael.plante@gmail.com>
Copyright © 2011-2013 Hans de Goede <hdegoede@redhat.com>
Copyright © 2012-2013 Martin Pieuchot <mpi@openbsd.org>
Copyright © 2012-2013 Toby Gray <toby.gray@realvnc.com>
Copyright © 2013-2018 Chris Dickens <christopher.a.dickens@gmail.com>`,
		bsd3,
	})
}
