package core

import (
	"github.com/pkg/errors"
	"log"
	"time"

	utils "github.com/Varunram/essentials/utils"
	xlm "github.com/YaleOpenLab/openx/chains/xlm"
	assets "github.com/YaleOpenLab/openx/chains/xlm/assets"
	escrow "github.com/YaleOpenLab/openx/chains/xlm/escrow"
	issuer "github.com/YaleOpenLab/openx/chains/xlm/issuer"
	wallet "github.com/YaleOpenLab/openx/chains/xlm/wallet"

	consts "github.com/YaleOpenLab/opensolar/consts"
	notif "github.com/YaleOpenLab/opensolar/notif"
	oracle "github.com/YaleOpenLab/opensolar/oracle"
)

// contains one of the two main contracts behind opensolar

// VerifyBeforeAuthorizing verifies information on the originator before upgrading the project stage
func VerifyBeforeAuthorizing(projIndex int) bool {
	project, err := RetrieveProject(projIndex)
	if err != nil {
		return false
	}
	originator, err := RetrieveEntity(project.OriginatorIndex)
	if err != nil {
		return false
	}
	log.Println("ORIGINATOR'S NAME IS:", originator.U.Name, " and PROJECT's METADATA IS: ", project.Metadata)
	if originator.U.Kyc && !originator.U.Banned {
		return true
	}
	return false
}

// RecipientAuthorize allows a recipient to authorize a specific project
func RecipientAuthorize(projIndex int, recpIndex int) error {
	project, err := RetrieveProject(projIndex)
	if err != nil {
		return errors.Wrap(err, "couldn't retrieve project")
	}
	if project.Stage != 0 {
		return errors.New("Project stage not zero")
	}
	if !VerifyBeforeAuthorizing(projIndex) {
		return errors.New("Originator not verified")
	}
	recipient, err := RetrieveRecipient(recpIndex)
	if err != nil {
		return errors.Wrap(err, "couldn't retrieve recipient")
	}
	if project.RecipientIndex != recipient.U.Index {
		return errors.New("You can't authorize a project which is not assigned to you!")
	}

	err = project.SetStage(1) // set the project as originated
	if err != nil {
		return errors.Wrap(err, "Error while setting origin project")
	}

	err = RepOriginatedProject(project.OriginatorIndex, project.Index)
	if err != nil {
		return errors.Wrap(err, "error while increasing reputation of originator")
	}

	return nil
}

// VoteTowardsProposedProject is a handler that an investor would use to vote towards a
// specific proposed project on the platform.
func VoteTowardsProposedProject(invIndex int, votes float64, projectIndex int) error {
	inv, err := RetrieveInvestor(invIndex)
	if err != nil {
		return errors.Wrap(err, "couldn't retrieve investor")
	}
	if votes > inv.VotingBalance {
		return errors.New("Can't vote with an amount greater than available balance")
	}

	project, err := RetrieveProject(projectIndex)
	if err != nil {
		return errors.Wrap(err, "couldn't retrieve project")
	}
	if project.Stage != 2 {
		return errors.New("You can't vote for a project with stage not equal to 2")
	}

	project.Votes += votes
	err = project.Save()
	if err != nil {
		return errors.Wrap(err, "couldn't save project")
	}

	err = inv.ChangeVotingBalance(votes)
	if err != nil {
		return errors.Wrap(err, "error while deducitng voting balance of investor")
	}

	log.Println("CAST VOTE TOWARDS PROJECT SUCCESSFULLY")
	return nil
}

// preInvestmentCheck associated with the opensolar platform when an Investor bids an investment amount of a specific project
func preInvestmentCheck(projIndex int, invIndex int, invAmount float64, seed string) (Project, error) {
	var project Project
	var investor Investor
	var err error

	project, err = RetrieveProject(projIndex)
	if err != nil {
		return project, errors.Wrap(err, "couldn't retrieve project")
	}

	investor, err = RetrieveInvestor(invIndex)
	if err != nil {
		return project, errors.Wrap(err, "couldn't retrieve investor")
	}

	if !investor.CanInvest(invAmount) {
		return project, errors.New("Investor has less balance than what is required to invest in this project")
	}

	pubkey, err := wallet.ReturnPubkey(seed)
	if err != nil {
		return project, errors.Wrap(err, "could not get pubkey from seed")
	}

	if !xlm.AccountExists(pubkey) {
		return project, errors.New("account doesn't exist yet, quitting")
	}
	// check if investment amount is greater than or equal to the project requirements
	if invAmount > project.TotalValue-project.MoneyRaised {
		return project, errors.New("Investment amount greater than what is required! Adjust your investment")
	}

	// the checks till here are common for all chains. The stuff following this is exclusive to stellar.
	if project.Chain == "stellar" || project.Chain == "" {
		if project.SeedAssetCode == "" && project.InvestorAssetCode == "" {
			// this project does not have an asset issuer associated with it yet since there has been
			// no seed round nor investment round
			project.InvestorAssetCode = assets.AssetID(consts.InvestorAssetPrefix + project.Metadata) // creat investor asset
			err = project.Save()
			if err != nil {
				return project, errors.Wrap(err, "couldn't save project")
			}
			err = issuer.InitIssuer(consts.OpenSolarIssuerDir, projIndex, consts.IssuerSeedPwd) // start an issuer with the projIndex
			if err != nil {
				return project, errors.Wrap(err, "error while initializing issuer")
			}
			err = issuer.FundIssuer(consts.OpenSolarIssuerDir, projIndex, consts.IssuerSeedPwd, consts.PlatformSeed) // fund the issuer since it needs to issue assets
			if err != nil {
				return project, errors.Wrap(err, "error while funding issuer")
			}
		}
		return project, nil
	} else if project.Chain == "algorand" {
		return project, errors.Wrap(err, "algorand investments not supported yet, quitting")
	} else {
		return project, errors.Wrap(err, "chain not supported, quitting")
	}
}

// SeedInvest is the seed investment function of the opensolar platform
func SeedInvest(projIndex int, invIndex int, invAmount float64, invSeed string) error {

	project, err := preInvestmentCheck(projIndex, invIndex, invAmount, invSeed)
	if err != nil {
		return errors.Wrap(err, "error while performing pre investment check")
	}

	if project.Stage != 1 && project.Stage != 2 {
		return errors.New("project stage not at 1, you either have passed the seed stage or project is not at seed stage yet")
	}

	if project.InvestmentType != "munibond" {
		return errors.New("investment models other than munibonds are not supported right now, quitting")
	}

	if project.SeedInvestmentCap < invAmount {
		return errors.New("you can't invest more than what the seed investment cap permits you to, quitting")
	}

	if project.Chain == "stellar" || project.Chain == "" {
		if project.SeedAssetCode == "" {
			log.Println("assigning a seed asset code")
			project.SeedAssetCode = "SEEDASSET" // set this to a constant asset for now
		}
		err = MunibondInvest(consts.OpenSolarIssuerDir, invIndex, invSeed, invAmount, projIndex,
			project.SeedAssetCode, project.TotalValue, project.SeedInvestmentFactor, true)
		if err != nil {
			return errors.Wrap(err, "error while investing")
		}

		err = project.updateAfterInvestment(invAmount, invIndex, true)
		if err != nil {
			return errors.Wrap(err, "couldn't update project after investment")
		}

		return err
	}

	return errors.New("other chain investments not supported  yet")
}

// Invest is the main invest function of the opensolar platform
func Invest(projIndex int, invIndex int, invAmount float64, invSeed string) error {
	var err error

	// run preinvestment checks
	project, err := preInvestmentCheck(projIndex, invIndex, invAmount, invSeed)
	if err != nil {
		return errors.Wrap(err, "pre investment check failed")
	}

	if project.InvestmentType != "munibond" {
		return errors.New("other investment models are not supported right now, quitting")
	}

	if project.Chain == "stellar" || project.Chain == "" {
		if project.Stage != 4 {
			if project.Stage == 1 || project.Stage == 2 {
				// investment is in seed stage
				return SeedInvest(projIndex, invIndex, invAmount, invSeed)
			}
			return errors.New("project not at stage where it can solicit investment, quitting")
		}

		err = MunibondInvest(consts.OpenSolarIssuerDir, invIndex, invSeed, invAmount, projIndex,
			project.InvestorAssetCode, project.TotalValue, 1, false)
		if err != nil {
			return errors.Wrap(err, "error while investing")
		}

		// once the investment is complete, update the project and store in the database
		err = project.updateAfterInvestment(invAmount, invIndex, false)
		if err != nil {
			return errors.Wrap(err, "failed to update project after investment")
		}
		return err
	}

	return errors.New("other chain investments not supported right now")
}

// updateAfterInvestment updates project db params after investment
func (project *Project) updateAfterInvestment(invAmount float64, invIndex int, seed bool) error {
	var err error
	project.MoneyRaised += invAmount
	if seed {
		project.SeedMoneyRaised += invAmount * (project.SeedInvestmentFactor - 1)
	}
	project.InvestorIndices = append(project.InvestorIndices, invIndex)

	err = project.Save()
	if err != nil {
		return errors.Wrap(err, "couldn't save project")
	}

	if project.MoneyRaised == project.TotalValue {
		// project has raised the entire amount that it needs. Set lock to true and wait for recipient's response
		project.Lock = true
		err = project.Save()
		if err != nil {
			return errors.Wrap(err, "couldn't save project")
		}

		// send the recipient a notification that his project has been funded
		err = project.sendRecipientNotification()
		if err != nil {
			return errors.Wrap(err, "error while sending notifications to recipient")
		}

		// start a goroutine that waits for the recipient to unlock the project
		go sendRecipientAssets(project.Index)
	}

	if len(project.InvestorMap) == 0 {
		project.InvestorMap = make(map[string]float64)
	}

	log.Println("INVESTOR INDICES: ", project.InvestorIndices)
	for i := range project.InvestorIndices {
		investor, err := RetrieveInvestor(project.InvestorIndices[i])
		if err != nil {
			return errors.Wrap(err, "error while retrieving investors, quitting")
		}

		log.Println(investor.U.StellarWallet.PublicKey, project.InvestorAssetCode)

		var balance1 float64
		var balance2 float64

		balance1, err = xlm.GetAssetBalance(investor.U.StellarWallet.PublicKey, project.InvestorAssetCode)
		if err != nil {
			balance1 = 0
		}

		balance2, err = xlm.GetAssetBalance(investor.U.StellarWallet.PublicKey, project.SeedAssetCode)
		if err != nil {
			balance2 = 0
		}

		balance := balance1 + balance2
		percentageInvestment := balance / project.TotalValue
		project.InvestorMap[investor.U.StellarWallet.PublicKey] = percentageInvestment
	}

	err = project.Save()
	log.Println("INVESTOR MAP: ", project.InvestorMap)
	if err != nil {
		return errors.Wrap(err, "error while saving project, quitting")
	}
	return nil
}

// sendRecipientNotification sends the notification to the recipient requesting them
// to logon to the platform and unlock the project that has just been invested in
func (project *Project) sendRecipientNotification() error {
	var recipient Recipient
	var err error

	recipient, err = RetrieveRecipient(project.RecipientIndex)
	if err != nil {
		// don't stop execution here, send an email to platform admin
		notif.SendRecpNotFoundEmail(project.Index, project.RecipientIndex)
		time.Sleep(consts.OneHour)
		recipient, err = RetrieveRecipient(project.RecipientIndex)
		if err != nil {
			return errors.Wrap(err, "couldn't retrieve recipient")
		}
	}
	notif.SendUnlockNotifToRecipient(project.Index, recipient.U.Email)
	return nil
}

// UnlockProject unlocks a specific project that has just been invested in
func UnlockProject(username string, pwhash string, projIndex int, seedpwd string) error {
	log.Println("UNLOCKING PROJECT")
	project, err := RetrieveProject(projIndex)
	if err != nil {
		return errors.Wrap(err, "couldn't retrieve project")
	}

	recipient, err := ValidateRecipient(username, pwhash)
	if err != nil {
		return errors.Wrap(err, "couldn't validate recipient")
	}

	if recipient.U.Index != project.RecipientIndex {
		return errors.New("Recipient Indices don't match, quitting!")
	}

	recpSeed, err := wallet.DecryptSeed(recipient.U.StellarWallet.EncryptedSeed, seedpwd)
	if err != nil {
		return errors.Wrap(err, "error while decrpyting seed")
	}

	checkPubkey, err := wallet.ReturnPubkey(recpSeed)
	if err != nil {
		return errors.Wrap(err, "couldn't get public key from seed")
	}

	if checkPubkey != recipient.U.StellarWallet.PublicKey {
		log.Println("Invalid seed")
		return errors.New("Failed to unlock project")
	}

	if !project.Lock {
		return errors.New("Project not locked")
	}

	project.LockPwd = seedpwd
	project.Lock = false
	err = project.Save()
	if err != nil {
		return errors.Wrap(err, "couldn't save project")
	}
	return nil
}

// sendRecipientAssets sends a recipient the debt asset and the payback asset associated with
// the opensolar platform
func sendRecipientAssets(projIndex int) error {
	startTime := utils.Unix()
	project, err := RetrieveProject(projIndex)
	if err != nil {
		return errors.Wrap(err, "Couldn't retrieve project")
	}

	for utils.Unix()-startTime < consts.LockInterval {
		log.Printf("WAITING FOR PROJECT %d TO BE UNLOCKED", projIndex)
		project, err = RetrieveProject(projIndex)
		if err != nil {
			return errors.Wrap(err, "Couldn't retrieve project")
		}
		if !project.Lock {
			log.Println("Project UNLOCKED IN LOOP")
			break
		}
		time.Sleep(10 * time.Second)
	}

	// lock is open, retrieve project and transfer assets
	project, err = RetrieveProject(projIndex)
	if err != nil {
		return errors.Wrap(err, "Couldn't retrieve project")
	}

	recipient, err := RetrieveRecipient(project.RecipientIndex)
	if err != nil {
		return errors.Wrap(err, "couldn't retrieve recipienrt")
	}

	recpSeed, err := wallet.DecryptSeed(recipient.U.StellarWallet.EncryptedSeed, project.LockPwd)
	if err != nil {
		return errors.Wrap(err, "couldn't decrypt seed")
	}

	escrowPubkey, err := escrow.InitEscrow(project.Index, consts.EscrowPwd, recipient.U.StellarWallet.PublicKey, recpSeed, consts.PlatformSeed)
	if err != nil {
		return errors.Wrap(err, "error while initializing issuer")
	}

	log.Println("successfully setup escrow")
	project.EscrowPubkey = escrowPubkey
	// transfer totalValue to the escrow, don't account for SeedMoneyRaised here
	err = escrow.TransferFundsToEscrow(project.TotalValue, project.Index, project.EscrowPubkey, consts.PlatformSeed)
	if err != nil {
		log.Println(err)
		return errors.Wrap(err, "could not transfer funds to the escrow, quitting!")
	}

	log.Println("Transferred funds to escrow!")
	project.LockPwd = "" // set lockpwd to nil immediately after retrieving seed

	project.DebtAssetCode = assets.AssetID(consts.DebtAssetPrefix + project.Metadata)
	project.PaybackAssetCode = assets.AssetID(consts.PaybackAssetPrefix + project.Metadata)

	// when sending debt and payback assets, account for SeedMoneyRaised
	err = MunibondReceive(consts.OpenSolarIssuerDir, project.RecipientIndex, projIndex, project.DebtAssetCode,
		project.PaybackAssetCode, project.EstimatedAcquisition, recpSeed, project.TotalValue+project.SeedMoneyRaised, project.PaybackPeriod)
	if err != nil {
		return errors.Wrap(err, "error while receiving assets from issuer on recipient's end")
	}

	err = project.updateProjectAfterAcceptance()
	if err != nil {
		return errors.Wrap(err, "failed to update project after acceptance of asset")
	}

	return nil
}

// updateProjectAfterAcceptance updates the project after the recipient accepts investment into the project
func (project *Project) updateProjectAfterAcceptance() error {

	// update balleft with SeedMoneyRaised
	project.BalLeft = project.TotalValue + project.SeedMoneyRaised // to carry over the extra returns that seed investors get
	project.Stage = Stage5.Number                                  // set to stage 5 (after the raise is done, we need to wait for people to construct the solar panels)

	err := project.Save()
	if err != nil {
		return errors.Wrap(err, "couldn't save project")
	}

	go monitorPaybacks(project.RecipientIndex, project.Index)
	return nil
}

// Payback pays the platform back in STABLEUSD and DebtAsset and receives PaybackAssets
// in return. Price to be paid per month depends on the electricity consumed by the recipient
// in the particular time frame.

// Payback is called by the recipient when they choose to pay towards the project according to the payback interval
func Payback(recpIndex int, projIndex int, assetName string, amount float64, recipientSeed string) error {

	project, err := RetrieveProject(projIndex)
	if err != nil {
		return errors.Wrap(err, "Couldn't retrieve project")
	}

	if project.InvestmentType != "munibond" {
		return errors.New("other investment models are not supported right now, quitting")
	}

	pct, err := MunibondPayback(consts.OpenSolarIssuerDir, recpIndex, amount,
		recipientSeed, projIndex, assetName, project.InvestorIndices, project.TotalValue, project.EscrowPubkey)
	if err != nil {
		return errors.Wrap(err, "Error while paying back the issuer")
	}

	project.BalLeft -= (1 - pct) * amount // the balance left should be the percenteage paid towards the asset, which is the monthly bill. THe re st goes into  ownership
	project.AmountOwed -= amount          // subtract the amount owed so we can track progress of payments in the monitorPaybacks loop
	project.OwnershipShift += pct
	project.DateLastPaid = utils.Unix()

	if project.BalLeft == 0 {
		log.Println("YOU HAVE PAID OFF THIS ASSET's LOAN, TRANSFERRING FUTURE PAYMENTS AS OWNERSHIP ASSETS OWNERSHIP OF ASSET TO YOU")
		project.Stage = 9
	}

	if project.OwnershipShift == 1 {
		// the recipient has paid off the asset completely
		log.Println("You now own the asset completely, there is no need to pay money in the future towards this particular project")
		project.BalLeft = 0
		project.AmountOwed = 0
	}

	err = project.Save()
	if err != nil {
		return errors.Wrap(err, "coudln't save project")
	}

	// TODO: we need to distribute funds which were paid back to all the parties involved, but we do so only for the investor here
	err = DistributePayments(recipientSeed, project.EscrowPubkey, projIndex, amount)
	if err != nil {
		return errors.Wrap(err, "error while distributing payments")
	}

	return nil
}

// DistributePayments distributes the return promised as part of the project back to investors and pays the other entities involved in the project
func DistributePayments(recipientSeed string, escrowPubkey string, projIndex int, amount float64) error {
	// this should act as the service which redistributes payments received out to the parties involved
	// amount is the amount that we want to give back to the investors and other entities involved
	project, err := RetrieveProject(projIndex)
	if err != nil {
		errors.Wrap(err, "couldn't retrieve project, quitting!")
	}

	if project.EscrowLock {
		log.Println("project", project.Index, "'s escrow locked, can't send funds")
		return errors.New("project escrow locked, can't send funds")
	}

	var fixedRate float64
	if project.InterestRate != 0 {
		fixedRate = project.InterestRate
	} else {
		fixedRate = 0.05 // 5 % interest rate if rate not defined
	}

	amountGivenBack := fixedRate * amount
	for pubkey, percentage := range project.InvestorMap {
		txAmount := percentage * amountGivenBack
		// here we send funds from the 2of2 multisig. Platform signs by default
		err = escrow.SendFundsFromEscrow(project.EscrowPubkey, pubkey, recipientSeed, consts.PlatformSeed, txAmount, "returns")
		if err != nil {
			log.Println("Error with payback to pubkey: ", pubkey, err) // if there is an error with one payback, doesn't mean we should stop and wait for the others
			continue
		}
	}
	return nil
}

// CalculatePayback calculates the amount of payback assets that must be issued in relation
// to the total amount invested in the project
func (project Project) CalculatePayback(amount float64) float64 {
	amountPB := (amount / project.TotalValue) * float64(project.EstimatedAcquisition*12)
	return amountPB
}

// monitorPaybacks monitors whether the user is paying back regularly towards the given project
// thread has to be isolated since if this fails, we stop tracking paybacks by the recipient.
func monitorPaybacks(recpIndex int, projIndex int) {
	for {
		project, err := RetrieveProject(projIndex)
		if err != nil {
			log.Println("Couldn't retrieve project")
			time.Sleep(consts.OneWeekInSecond)
		}

		recipient, err := RetrieveRecipient(recpIndex)
		if err != nil {
			log.Println("Couldn't retrieve recipient")
			time.Sleep(consts.OneWeekInSecond)
		}

		guarantor, err := RetrieveEntity(project.GuarantorIndex)
		if err != nil {
			log.Println("couldn't retrieve guarantor")
			time.Sleep(consts.OneWeekInSecond)
		}

		period := float64(time.Duration(project.PaybackPeriod) * consts.OneWeekInSecond) // in seconds due to the const
		if period == 0 {
			period = 1 // for the test suite
		}
		timeElapsed := utils.Unix() - project.DateLastPaid // this would be in seconds (unix time)
		factor := float64(timeElapsed) / period
		project.AmountOwed += factor * oracle.MonthlyBill() // add the amount owed only if the time elapsed is more than one payback period
		// Reputation adjustments based on payback history:
		if factor <= 1 {
			// don't do anything since the user has been paying back regularly
			log.Println("User: ", recipient.U.Email, "is on track paying towards order: ", projIndex)
			// maybe even update reputation here on a fractional basis depending on a user's timely payments
		} else if factor > NormalThreshold && factor < AlertThreshold {
			// person has not paid back for one-two consecutive period, send gentle reminder
			notif.SendNicePaybackAlertEmail(projIndex, recipient.U.Email)
			time.Sleep(consts.OneWeekInSecond)
		} else if factor >= SternAlertThreshold && factor < DisconnectionThreshold {
			// person has not paid back for four consecutive cycles, send reminder
			notif.SendSternPaybackAlertEmail(projIndex, recipient.U.Email)
			for _, i := range project.InvestorIndices {
				// send an email to recipients to assure them that we're on the issue and will be acting
				// soon if the recipient fails to pay again.
				investor, err := RetrieveInvestor(i)
				if err != nil {
					log.Println(err)
					continue
				}
				if investor.U.Notification {
					notif.SendSternPaybackAlertEmailI(projIndex, investor.U.Email)
				}
			}
			notif.SendSternPaybackAlertEmailG(projIndex, guarantor.U.Email)
			time.Sleep(consts.OneWeekInSecond)
		} else if factor >= DisconnectionThreshold {
			// send a disconnection notice to the recipient and let them know we have redirected
			// power towards the grid.
			for _, i := range project.InvestorIndices {
				// send an email to investors on teller disconnection
				investor, err := RetrieveInvestor(i)
				if err != nil {
					log.Println(err)
					time.Sleep(consts.OneWeekInSecond)
					continue
				}
				if investor.U.Notification {
					notif.SendDisconnectionEmailI(projIndex, investor.U.Email)
				}
			}
			// we have sent out emails to investors, send an email to the guarantor and cover first losses of investors
			notif.SendDisconnectionEmailG(projIndex, guarantor.U.Email)
			err = CoverFirstLoss(project.Index, guarantor.U.Index, project.AmountOwed)
			if err != nil {
				log.Println(err)
				time.Sleep(consts.OneWeekInSecond)
				continue
			}
		}

		time.Sleep(consts.OneWeekInSecond) // poll every week to check progress on payments
	}
}

// addWaterfallAccount adds a waterfall account that the recipient must payback towards
func addWaterfallAccount(projIndex int, pubkey string, amount float64) error {
	project, err := RetrieveProject(projIndex)
	if err != nil {
		return errors.Wrap(err, "could not retrieve project, quitting")
	}
	if project.WaterfallMap == nil {
		project.WaterfallMap = make(map[string]float64)
	}
	project.WaterfallMap[pubkey] = amount
	return project.Save()
}

// CoverFirstLoss covers first loss for investors by sending funds from the guarantor's account
func CoverFirstLoss(projIndex int, entityIndex int, amount float64) error {
	// cover first loss for the project specified
	project, err := RetrieveProject(projIndex)
	if err != nil {
		return errors.Wrap(err, "could not retrieve projects from database, quitting")
	}

	entity, err := RetrieveEntity(entityIndex)
	if err != nil {
		return errors.Wrap(err, "could not retrieve entity from database, quitting")
	}

	if project.GuarantorIndex != entity.U.Index {
		return errors.New("guarantor index does not match with entity's index in database")
	}

	if entity.FirstLossGuaranteeAmt < amount {
		log.Println("amount required greater than what guarantor agreed to provide, adjusting first loss to cover for what's available")
		amount = entity.FirstLossGuaranteeAmt
	}
	// we now need to send funds from the gurantor's account to the escrow
	seed, err := wallet.DecryptSeed(entity.U.StellarWallet.EncryptedSeed, entity.FirstLossGuarantee) //
	if err != nil {
		return errors.Wrap(err, "could not decrypt seed, quitting!")
	}

	var txhash string
	// we have the escrow's pubkey, transfer funds to the escrow
	if !consts.Mainnet {
		_, txhash, err = assets.SendAsset(consts.StablecoinCode, consts.StablecoinPublicKey, project.EscrowPubkey, amount, seed, "first loss guarantee")
		if err != nil {
			return errors.Wrap(err, "could not transfer asset to escrow, quitting")
		}
	} else {
		_, txhash, err = assets.SendAsset(consts.AnchorUSDCode, consts.AnchorUSDAddress, project.EscrowPubkey, amount, seed, "first loss guarantee")
		if err != nil {
			return errors.Wrap(err, "could not transfer asset to escrow, quitting")
		}
	}

	log.Println("txhash of guarantor kick in:", txhash)

	return nil
}
