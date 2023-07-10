# go-lib-mbox2nntp

This go-lib-mbox2nntp package reads many of the MBOX-formatted usenet archives found at archive.org

[mbox2nntp](https://github.com/go-while/mbox2nntp) uses this lib to read .mbox[.(7z|gz|zip)] archives and send them via IHAVE to a nntp backend.

## Forked from [https://github.com/sam-falvo/mbox](https://github.com/sam-falvo/mbox)

Fixed bug where headers were stored in a map which resulted in scrambled header lines because go maps are not ordered.
