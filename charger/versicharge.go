package charger

// LICENSE

// Copyright (c) 2019-2022 andig => Vertel.go Charger used as basis
//                                  Additional input from other EVCC Charger GO templates (e.g. ABB)
// Copyright (c) 2022 achgut/Flo56958 => Change and adpation to Versicharge Gen 3 Charger

// This module is NOT covered by the MIT license. All rights reserved.

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

//************************************************************************************

// Verwendete Versicharge GEN 3:
  // Versicharge GEN3 FW 2.120 oder höher
  // Commercial Version (Reg 22 = 2), One Outlet: (Reg 24 = 1)
  // Integrated MID (Reg 30 = 4)
  // Test mit Order Number: 8EM1310-3EJ04-0GA0

  //https://support.industry.siemens.com/cs/attachments/109814359/versicharge_wallbox_modBus_map_en-US-FINAL.pdf

  // Gefundene Fehler:
    // - Status Wallbox (A-F): Register 1601 nicht im ModbusMap dokumentiert. 
	// - Active Power Phase Sum wird bei Strömen über 10A falsch berechnet (Register 1665)
    //   daher Verwendung Apparent Power.
	// - MaxCurrent wird um 1A reduziert (bekanntes Problem), gilt nicht für 8A, 16A, 24A, 32A
//************************************************************************************

// Weitere zukünfitge Themen zu implementieren / testen:

  // Laden 1/3 Phasen
  // 1 und 3 phasiges Laden implentmentiert aber funktioniert nicht 
  // Trotz Umschaltung und Strom-Werte 1 phasig wird weiterhin mit 3 phasen physisch geladen

  //RFID
  // Im ModbusTable fehlt das Register welche Karte freigegen wurde (zur Fahrzeugerkennung)
 	//  VersichargeRegRFIDEnable        = 79 // 1 RW disabled: 0 , enabled: 1	
	//	VersichargeRegRFIDCount         = 87 // 1  RO
    //	VersichargeRegRFID_UID0         = 88 // 5  RO
    //	VersichargeRegRFID_UID1         = 93 // 5  RO
    //	VersichargeRegRFID_UID2         = 97 // 5  RO
    //  weitere RFID Karten möglich (bis Register 337)

//  Failsafe Current und Timeout
    //  VersichargeRegFailsafeTimeout    = 1661 // RW 
    //  VersichargeRegFailsafeCurrentSum = 1660 // RW 

  // Time and Energy of charging session	 
    //  VersichargeRegSessionEnergy   = // derzeit nicht vorhanden im Modbus Table
    //	VersichargeRegChargeTime      = // derzeit nicht vorhanden im Modbus Table
	//  nur Total Energy (Gesamtladeleistung Wallbox) vorhanden

// Alive Check / Heartbeat Function (notwendig? aus ABB)
    //  VersichargeRegAlive           = // derzeit nicht vorhanden im Modbus Table

//************************************************************************************

import (
	"encoding/binary"
	"fmt"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/modbus"
)

const (
// Info Wallbox, nur Lesen
    VersichargeRegBrand             = 0    // 5   RO ASCII    -> Diagnose
	VersichargeRegProductionDate    = 5    // 2   RO UNIT16[] -> Diagnose
	VersichargeRegSerial            = 7    // 5   RO ASCII    -> Diagnose 
	VersichargeRegModel             = 12   // 10 RO ASCII     -> Diagnose
	VersichargeRegFirmware          = 31   // 10 RO ASCII     -> Diagnose
	VersichargeRegModbusTable       = 41   // 1  RO UINT16    -> Diagnose
	VersichargeRegRatedCurrent      = 28   // 1  RO UINT16    -> Diagnose
	VersichargeRegCurrentDipSwitch  = 29   // 1  RO UNIT16    -> Diagnose
	VersichargeRegMeterType         = 30   // 1  RO UINT16    -> Diagnose
	VersichargeRegTemp				= 1602 // 1  RO INT16     -> Diagnose

// Charger States / Settings / Steuerung
 	VersichargeRegRFIDEnable      =   79 // 1 RW UNIT16  -> disabled: 0 , enabled: 1	
    VersichargeRegChargeStatus    = 1601 // 1 RO INT16?? -> Status 1-5 nicht dokumentiert
    VersichargePause              = 1629 // 1 RW UNIT16  -> On: 1, Off: 2 - AN
    VersichargePhases             = 1642 // 1 RW UNIT16  -> 1Phase: 0 ; 3Phase: 1
	VersichargeRegMaxCurrent      = 1633 // 1 RW UNIT16  -> Max. Charging Current
    VersichargeRegTotalEnergy     = 1692 // 2 RO Unit32(BigEndian) 
                                         // -> Gesamtleistung Wallbox in WattHours (Mulitplikation mit 0,1)
)

var (
	VersichargeRegCurrents = []uint16{1647, 1648, 1649, 1650}  // L1, L2, L3, SUM in AMP
	VersichargeRegVoltages = []uint16{1651, 1652, 1653}        // L1-N, L2-N, L3-N in V
//	VersichargeRegPower    = []uint16{1662, 1663, 1664, 1665}  // L1, L2, L3, SUM in Watt (Actual Power)
                                                               // SUM (Multiplikation mit 0,1)  
                                                               // WB bringt teilweise falschen Summenwert (bei >10A)
	VersichargeRegPower    = []uint16{1670, 1671, 1672, 1673}  // L1, L2, L3, SUM in Watt (Aparent Power)
)

// Versicharge is an api.Charger implementation for Versicharge wallboxes with Ethernet (SW modells).
// It uses Modbus TCP to communicate with the wallbox at modbus client id 1. 


type Versicharge struct {
	log     *util.Logger
	conn    *modbus.Connection
	lp      loadpoint.API
	current uint16
}

func init() {
	registry.Add("versicharge", NewVersichargeFromConfig)
}

// NewVersichargeFromConfig creates a Versicharge charger from generic config
func NewVersichargeFromConfig(other map[string]interface{}) (api.Charger, error) {
	cc := modbus.TcpSettings{
		ID: 1,
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	return NewVersicharge(cc.URI, cc.ID)
}

// NewVersicharge creates a Versicharge charger
func NewVersicharge(uri string, id uint8) (*Versicharge, error) {
	conn, err := modbus.NewConnection(uri, "", "", 0, modbus.Tcp, id)
	if err != nil {
		return nil, err
	}

	log := util.NewLogger("versicharge")
	conn.Logger(log.TRACE)

	wb := &Versicharge{
		log:     log,
		conn:    conn,
		current: 6,
	}

// Check FW Version 2.120	
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegFirmware, 5); err == nil {
		fmt.Printf("[VERSI ] INFO Versicharge Firmware: \t%s \n", b)
		if bytesAsString(b) != "2.120" {
			fmt.Printf("[VERSI ] WARN Versicharge Firmware:\t%s -> Falsche Version, getestet mit FW 2.120 \n", b)
		}
	}

// MaxCurrent auf 6A setzen bei Initialisierung
	b, err := wb.conn.WriteSingleRegister(VersichargeRegMaxCurrent, 7) // Red. um 1A auf 6A -> known Bug
	if err == nil {
		wb.current = 6
		fmt.Printf("[VERSI ] INFO MaxCurrent auf 6A gesetzt")
		fmt.Printf(" (Bug %x) \n", b)
	} else {
		return wb, err	
	}

	return wb, nil
}


// ------------------------------------------------------------------------------------------------------
// Charger
// ------------------------------------------------------------------------------------------------------

// Status implements the api.Charger interface (Charging State A-F)
func (wb *Versicharge) Status() (api.ChargeStatus, error) {
	s, err := wb.conn.ReadHoldingRegisters(VersichargeRegChargeStatus, 1)
		if err != nil {
		return api.StatusNone, err
	}

	switch binary.BigEndian.Uint16(s) {
	case 1: // State A: Idle, Power on
		return api.StatusA, nil
	case 2: // State B: EV Plug in, pending authorization
		return api.StatusB, nil
	case 3: // Charging
		return api.StatusC, nil
	case 4: // Charging? kommt nur kurzzeitg beim Starten, dann Rückfall auf 3
		b, err := wb.conn.ReadHoldingRegisters(VersichargePause, 1) // Abfrage Pausiert?
		if err != nil {
			return api.StatusNone, err
		}
		if binary.BigEndian.Uint16(b) == 0x1 {  
			//Pause ON -> Fehlerfall/Issue: 
			//https://github.com/achgut/Modbus_Versicharge/issues/12#issue-1501793186
			return api.StatusNone, fmt.Errorf("invalid status during pause: %0x", s)
		}
		return api.StatusC, nil
	case 5: // Other: Session stopped (Pause) 
		b, err := wb.conn.ReadHoldingRegisters(VersichargePause, 1) // Abfrage Pausiert?
		if err != nil {
			return api.StatusNone, err
		}
		if binary.BigEndian.Uint16(b) == 0x1 {  //Pause ON
			return api.StatusB, nil
		}
	default: // Other
		return api.StatusNone, fmt.Errorf("invalid status: %0x", s)
	}
	return api.StatusNone, err
}

// Enabled implements the api.Charger interface -> Über Pause
func (wb *Versicharge) Enabled() (bool, error) {
	b, err := wb.conn.ReadHoldingRegisters(VersichargePause, 1)
	if err != nil {
		return false, err
	}

	return binary.BigEndian.Uint16(b) == 2, nil
}

// Enable implements the api.Charger interface
// Enable mit Einstellung auf MinCurrent sinnvoll?
func (wb *Versicharge) Enable(enable bool) error {
    var u uint16
	u = 1
	if enable == true {
		u = 2
		}
	_, err := wb.conn.WriteSingleRegister(VersichargePause, u)

	return err
}

// MaxCurrent implements the api.Charger interface (CurrentLimiter)
func (wb *Versicharge) MaxCurrent(current int64) error {
	if current < 6 {
		return fmt.Errorf("invalid current %d", current)
	}

	u := uint16(current)

	// Bug Korrektur -> Strom wird um 1A vermindert (außer bei 8, 16,24,32)
	if (u != 8 && u != 16 && u != 24 && u != 32) {
		u = u + 1  // Erhöhung um 1A
	}

	_, err := wb.conn.WriteSingleRegister(VersichargeRegMaxCurrent, u)
	if err == nil {
		wb.current = u
	}

	return err
}

// var _ api.PhaseSwitcher = (*Versicharge)(nil)
//
//// Phases1p3p implements the api.PhaseSwitcher interface 1Phase: 0 ; 3Phase: 1
//// Feature führt derzeit zum Absturz der Wallbox, nicht verwenden (in evcc.yaml: Phases = 3)
// func (wb *Versicharge) Phases1p3p(phases int) error {
// 	fmt.Printf("%d Phases Umschaltung \n", phases)
// 	
// 	if phases == 1 {
// 		_, err := wb.conn.WriteSingleRegister(VersichargePhases, uint16(0)) // 1 Phase = 0
// 		return err
// 	}
// 
// 	if phases == 3 {
// 		_, err := wb.conn.WriteSingleRegister(VersichargePhases, uint16(1)) // 3 Phasen = 1
// 		return err
// 	}
// 
// 	return nil 
// }

// ------------------------------------------------------------------------------------------------------
// Meter
// ------------------------------------------------------------------------------------------------------
var _ api.Meter = (*Versicharge)(nil)

// CurrentPower implements the api.Meter interface
func (wb *Versicharge) CurrentPower() (float64, error) {
	b, err := wb.conn.ReadHoldingRegisters(VersichargeRegPower[3], 1)
	if err != nil {
	  return 0, err
	}

	return float64(binary.BigEndian.Uint16(b)), err 
}

var _ api.PhaseCurrents = (*Versicharge)(nil)

// Currents implements the api.PhaseCurrents interface
func (wb *Versicharge) Currents() (float64, float64, float64, error) {
	var currents []float64
	for _, regCurrent := range VersichargeRegCurrents {
		b, err := wb.conn.ReadHoldingRegisters(regCurrent, 1)
		if err != nil {
			return 0, 0, 0, err
		}

		currents = append(currents, float64(binary.BigEndian.Uint16(b))) // in Ampere
	}

	return currents[0], currents[1], currents[2], nil
}

var _ api.PhaseVoltages = (*Versicharge)(nil)

// Voltages implements the api.PhaseVoltages interface, (noch?) nicht vorhanden (aus Alfen.go) 
func (wb *Versicharge) Voltages() (float64, float64, float64, error) {
	var voltages []float64
	for _, regVoltage := range VersichargeRegVoltages {
		b, err := wb.conn.ReadHoldingRegisters(regVoltage, 1)
		if err != nil {
			return 0, 0, 0, err
		}

		voltages = append(voltages, float64(binary.BigEndian.Uint16(b))) // in Volt
	}

	return voltages[0], voltages[1], voltages[2], nil
}

var _ api.PhasePowers = (*Versicharge)(nil)

// Voltages implements the api.PhasePowers interface, (noch?) nicht vorhanden (aus Alfen.go) 
func (wb *Versicharge) Powers() (float64, float64, float64, error) {
	var powers []float64
	for _, regPower := range VersichargeRegPower {
		b, err := wb.conn.ReadHoldingRegisters(regPower, 1)
		if err != nil {
			return 0, 0, 0, err
		}

		powers = append(powers, float64(binary.BigEndian.Uint16(b))) // in Watt
	}

	return powers[0], powers[1], powers[2], nil
}

var _ api.MeterEnergy = (*Versicharge)(nil)

// TotalEnergy implements the api.MeterEnergy interface
func (wb *Versicharge) TotalEnergy() (float64, error) {
	b, err := wb.conn.ReadHoldingRegisters(VersichargeRegTotalEnergy, 2)
	if err != nil {
		return 0, err
	}

	return float64(binary.BigEndian.Uint32(b)) / 10000, err
}

// ------------------------------------------------------------------------------------------------------
// Identifier -> Erkennung Auto am Ladepunkt durch RFID Karte
// Authorizer verwenden?
// ------------------------------------------------------------------------------------------------------

// var _ api.Identifier = (*Versicharge)(nil)
// 
// // Identify implements the api.Identifier interface
// // experimental, zum Test. Noch falsches Register (Brand wird gelesen)
// // aus Template WebastoNext Charger (Webasto-next)
// func (wb *Versicharge) Identify() (string, error) {
// 	b, err := wb.conn.ReadHoldingRegisters(VersichargeRegBrand, 5)
// 	fmt.Printf("Identifier (Func Identify): ", bytesAsString(b), "/n")
// 	if err != nil {
// 		return "", err
// 	}
// 
// 	return bytesAsString(b), nil
// }

var _ loadpoint.Controller = (*Versicharge)(nil)

// LoadpointControl implements loadpoint.Controller
// Funktion?
func (wb *Versicharge) LoadpointControl(lp loadpoint.API) {
	wb.lp = lp
}

// ------------------------------------------------------------------------------------------------------
// Diagnoses
// ------------------------------------------------------------------------------------------------------

var _ api.Diagnosis = (*Versicharge)(nil)

// Diagnose implements the api.Diagnosis interface
func (wb *Versicharge) Diagnose() {
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegBrand, 5); err == nil {
		fmt.Printf("Brand:\t\t\t%s\n", b) 
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegModel, 10); err == nil {
		fmt.Printf("Model:\t\t\t%s\n", b) 
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegSerial, 5); err == nil {
		fmt.Printf("Serial:\t\t\t%s\n", b)
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegProductionDate, 2); err == nil {
		fmt.Printf("Production Date:\t%d.%d.%d\n", b[3], b[2], binary.BigEndian.Uint16(b[0:2]))		
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegFirmware, 10); err == nil {
		fmt.Printf("Firmware:\t\t%s\n", b) 
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegModbusTable, 1); err == nil {
		fmt.Printf("Modbus Table:\t\t%d\n", b[1]) 
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegRatedCurrent, 1); err == nil {
		fmt.Printf("Rated Current:\t\t%d\n", b[1]) 
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegCurrentDipSwitch, 1); err == nil {
		fmt.Printf("Current (DIP Switch):\t%d\n", b[1]) 
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegMeterType, 1); err == nil {
		fmt.Printf("Meter Type:\t\t%d\n", b[1])
	}
	if b, err := wb.conn.ReadHoldingRegisters(VersichargeRegTemp, 1); err == nil {
	    fmt.Printf("Temperature PCB:\t%d°C\n\n", b[1])
    }
}